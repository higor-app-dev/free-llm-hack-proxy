#!/usr/bin/env python3
"""FIFO queue with slot management and timeout for the free-llm-hack-proxy.

When all provider slots are occupied, incoming requests are enqueued in
strict FIFO order.  If a request stays in the queue longer than the
configured timeout (default: 30 seconds), it is rejected with 503.

Usage::

    from src.proxy.queue import SlottedQueue, SlotTicket

    queue = SlottedQueue(max_slots=10, queue_timeout=30.0)
    await queue.start()          # boots the background timeout watcher

    ticket = await queue.acquire(timeout=30.0)
    if not ticket.valid:
        return {"error": "request timed out in queue"}, 503
    try:
        ... process request ...
    finally:
        await queue.release()

    await queue.stop()           # cancels the background timeout watcher
"""

from __future__ import annotations

import asyncio
import logging
import time
from collections import deque
from dataclasses import dataclass, field
from typing import Optional
from uuid import uuid4

from src.proxy.metrics import queue_depth

logger = logging.getLogger("proxy.queue")


# ---------------------------------------------------------------------------
# SlotTicket — returned by acquire()
# ---------------------------------------------------------------------------


@dataclass
class SlotTicket:
    """Represents the result of attempting to acquire a processing slot.

    Attributes
    ----------
    valid :
        ``True`` if a slot was granted, ``False`` if the request timed out.
    reason :
        Human-readable explanation when *valid* is ``False``.
    request_id :
        Unique identifier for this queued request (set only when queued).
    """

    valid: bool = True
    reason: str = ""
    request_id: str = ""


# ---------------------------------------------------------------------------
# QueueRequest — internal item stored in the FIFO deque
# ---------------------------------------------------------------------------


@dataclass
class QueueRequest:
    """One item waiting in the FIFO queue.

    When a slot becomes available the *future* is resolved with a
    ``SlotTicket``; when the request expires the future receives a
    ``TimeoutError``.

    .. note::

       The *future* is created lazily in ``acquire()`` because
       ``asyncio.Future`` must be created inside a running event loop.
    """

    request_id: str = field(default_factory=lambda: uuid4().hex)
    enqueued_at: float = field(default_factory=time.monotonic)
    future: Optional[asyncio.Future] = None


# ---------------------------------------------------------------------------
# SlottedQueue — the main class
# ---------------------------------------------------------------------------


class SlottedQueue:
    """Async-compatible FIFO queue with a fixed number of processing slots.

    Parameters
    ----------
    max_slots :
        Maximum number of concurrent requests that can hold a slot.
    queue_timeout :
        Maximum number of seconds a request may wait in the queue before
        being rejected (503).
    """

    def __init__(
        self,
        max_slots: int = 10,
        queue_timeout: float = 30.0,
    ) -> None:
        if max_slots < 1:
            raise ValueError("max_slots must be >= 1")
        if queue_timeout <= 0:
            raise ValueError("queue_timeout must be > 0")

        self.max_slots = max_slots
        self.queue_timeout = queue_timeout

        self._active: int = 0
        self._lock = asyncio.Lock()
        self._queue: deque[QueueRequest] = deque()
        self._timeout_task: Optional[asyncio.Task] = None

    # ------------------------------------------------------------------
    # Lifecycle
    # ------------------------------------------------------------------

    async def start(self) -> None:
        """Start the background timeout watcher.

        Must be called before ``acquire()`` is used.  Idempotent.
        """
        if self._timeout_task is None or self._timeout_task.done():
            self._timeout_task = asyncio.create_task(self._timeout_watcher())
            logger.info(
                "SlottedQueue started (max_slots=%s, queue_timeout=%ss)",
                self.max_slots,
                self.queue_timeout,
            )

    async def stop(self) -> None:
        """Stop the background timeout watcher.  Idempotent."""
        if self._timeout_task and not self._timeout_task.done():
            self._timeout_task.cancel()
            try:
                await self._timeout_task
            except asyncio.CancelledError:
                pass
            self._timeout_task = None
            logger.info("SlottedQueue stopped")

    # ------------------------------------------------------------------
    # State inspection
    # ------------------------------------------------------------------

    @property
    def active(self) -> int:
        """Number of slots currently in use."""
        return self._active

    @property
    def queued(self) -> int:
        """Number of requests waiting in the queue."""
        return len(self._queue)

    @property
    def total_in_flight(self) -> int:
        """Active + queued requests."""
        return self._active + len(self._queue)

    @property
    def available_slots(self) -> int:
        """Number of slots that are free right now."""
        return max(0, self.max_slots - self._active)

    # ------------------------------------------------------------------
    # Core API
    # ------------------------------------------------------------------

    async def acquire(self, timeout: Optional[float] = None) -> SlotTicket:
        """Acquire a processing slot.

        Fast path: if a slot is immediately available, returns a valid
        ticket instantly.  Otherwise the caller is enqueued in FIFO
        order and waits for up to *timeout* seconds (defaults to
        ``self.queue_timeout``).

        Parameters
        ----------
        timeout :
            Per-call timeout override.  ``None`` uses the queue-wide
            *queue_timeout*.

        Returns
        -------
        SlotTicket
            *valid=True* with a slot granted, or *valid=False* with a
            reason if the request expired in the queue.
        """
        effective_timeout = self.queue_timeout if timeout is None else timeout

        # --- Fast path: slot available immediately ---
        async with self._lock:
            if self._active < self.max_slots:
                self._active += 1
                logger.debug("Acquired slot immediately (%s/%s active)", self._active, self.max_slots)
                return SlotTicket(valid=True)

            # --- Slow path: enqueue ---
            req = QueueRequest()
            req.future = asyncio.get_event_loop().create_future()
            self._queue.append(req)
            queue_depth.labels(route="/v1/chat/completions").set(len(self._queue))
            logger.debug(
                "Enqueued request %s (%s waiting)",
                req.request_id,
                len(self._queue),
            )

        # --- Wait outside the lock ---
        try:
            ticket = await asyncio.wait_for(req.future, timeout=effective_timeout)
            logger.debug("Request %s dequeued after waiting", req.request_id)
            return ticket
        except asyncio.TimeoutError:
            # Remove from queue if it hasn't been served yet
            async with self._lock:
                try:
                    self._queue.remove(req)
                    logger.info(
                        "Request %s expired after %ss (queue timeout)",
                        req.request_id,
                        effective_timeout,
                    )
                except ValueError:
                    # The request was already served between the timeout
                    # and the lock acquisition — this is extremely rare
                    # but safe: the future resolved, so the caller
                    # actually got a slot.  Return a valid ticket.
                    return SlotTicket(valid=True)
            queue_depth.labels(route="/v1/chat/completions").set(len(self._queue))

            return SlotTicket(valid=False, reason="request timed out in queue")

    async def release(self) -> None:
        """Release the current slot and dequeue the next waiting request.

        Must be called exactly once for every successful ``acquire()``
        that returned *valid=True*.
        """
        async with self._lock:
            self._active = max(0, self._active - 1)

            # Dequeue the next waiter (FIFO) and grant it a slot
            while self._queue:
                next_req = self._queue.popleft()
                if next_req.future.done():
                    # Already cancelled/timed-out — skip it
                    continue
                next_req.future.set_result(
                    SlotTicket(valid=True, request_id=next_req.request_id)
                )
                self._active += 1
                queue_depth.labels(route="/v1/chat/completions").set(len(self._queue))
                logger.debug(
                    "Dequeued request %s (%s waiting)",
                    next_req.request_id,
                    len(self._queue),
                )
                break  # only grant one slot per release

    # ------------------------------------------------------------------
    # Timeout watcher (background task)
    # ------------------------------------------------------------------

    async def _timeout_watcher(self) -> None:
        """Background coroutine that periodically scans the queue for
        expired requests and rejects them.

        Runs every 1 second — low overhead since it only acquires the
        lock for O(1) operations per expired item.
        """
        logger.debug("Timeout watcher started")
        try:
            while True:
                await asyncio.sleep(1.0)
                now = time.monotonic()
                expired: list[QueueRequest] = []

                async with self._lock:
                    # Scan from front (oldest) — stop at first non-expired
                    # because the queue is FIFO: if the front isn't expired,
                    # nothing behind it can be.
                    while self._queue:
                        front = self._queue[0]
                        age = now - front.enqueued_at
                        if age < self.queue_timeout:
                            break
                        expired.append(self._queue.popleft())
                    queue_depth.labels(route="/v1/chat/completions").set(len(self._queue))

                # Resolve expired futures OUTSIDE the lock to avoid
                # holding it during cancellation callbacks
                for req in expired:
                    if not req.future.done():
                        req.future.set_exception(asyncio.TimeoutError())
                        logger.info(
                            "Expired request %s (age=%.1fs)",
                            req.request_id,
                            now - req.enqueued_at,
                        )
        except asyncio.CancelledError:
            logger.debug("Timeout watcher stopped")
            raise
