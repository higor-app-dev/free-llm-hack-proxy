"""Tests for ``src.proxy.queue`` — FIFO queue with slot management and timeout.

Covers:
  1. Basic acquire/release lifecycle
  2. FIFO ordering under load
  3. Timeout watcher (expired requests return 503)
  4. No memory leaks from expired requests
  5. Edge cases (zero max_slots, negative timeout, concurrent stress)
"""

from __future__ import annotations

import asyncio
import time

import pytest

from src.proxy.queue import QueueRequest, SlottedQueue


# ===========================================================================
# Fixtures
# ===========================================================================


@pytest.fixture()
async def queue() -> SlottedQueue:
    """Create a small queue (3 slots, 1 s timeout) for fast tests."""
    q = SlottedQueue(max_slots=3, queue_timeout=1.0)
    await q.start()
    yield q
    await q.stop()


@pytest.fixture()
async def queue_single() -> SlottedQueue:
    """Single-slot queue for strict FIFO tests."""
    q = SlottedQueue(max_slots=1, queue_timeout=5.0)
    await q.start()
    yield q
    await q.stop()


# ===========================================================================
# Basic acquire / release
# ===========================================================================


class TestBasicAcquireRelease:
    """Verify the fundamental slot lifecycle."""

    async def test_acquire_returns_valid_ticket_when_slots_free(self, queue: SlottedQueue) -> None:
        """Slots are free → ticket is valid immediately."""
        ticket = await queue.acquire()
        assert ticket.valid
        assert ticket.reason == ""

    async def test_acquire_increments_active(self, queue: SlottedQueue) -> None:
        """Active count reflects slots in use."""
        assert queue.active == 0
        t1 = await queue.acquire()
        assert queue.active == 1
        t2 = await queue.acquire()
        assert queue.active == 2
        await queue.release()
        assert queue.active == 1
        await queue.release()
        assert queue.active == 0

    async def test_release_frees_slot(self, queue: SlottedQueue) -> None:
        """After release, available_slots goes back up."""
        t1 = await queue.acquire()
        t2 = await queue.acquire()
        t3 = await queue.acquire()
        assert queue.available_slots == 0

        await queue.release()  # free one slot
        assert queue.available_slots == 1

    async def test_release_with_no_waiters_does_nothing(self, queue: SlottedQueue) -> None:
        """Releasing when nothing is queued just decrements active."""
        await queue.acquire()
        assert queue.active == 1
        await queue.release()
        assert queue.active == 0
        # Extra release is a no-op (clamped to 0)
        await queue.release()
        assert queue.active == 0

    async def test_max_slots_enforced(self, queue: SlottedQueue) -> None:
        """Cannot exceed max_slots — extra requesters queue."""
        t1 = await queue.acquire()
        t2 = await queue.acquire()
        t3 = await queue.acquire()
        assert queue.active == 3
        assert queue.queued == 0

        # Fourth caller queues
        t4_task = asyncio.create_task(queue.acquire(timeout=0.5))
        await asyncio.sleep(0.05)
        assert queue.queued == 1
        assert queue.active == 3

        # Free a slot → queued request should complete
        await queue.release()
        t4 = await t4_task
        assert t4.valid

    async def test_fifo_ordering(self, queue_single: SlottedQueue) -> None:
        """Requests are processed strictly in arrival order."""
        # Fill the single slot
        await queue_single.acquire()
        assert queue_single.active == 1

        # Enqueue 3 waiters — they record their number and wait for manual release
        results: list[int] = []

        async def waiter(n: int) -> None:
            ticket = await queue_single.acquire(timeout=5.0)
            results.append(n)
            # Do NOT release here — the test releases manually

        tasks = [asyncio.create_task(waiter(i)) for i in range(3)]
        await asyncio.sleep(0.05)
        assert queue_single.queued == 3

        # Release the first slot → waiter 0 should get it
        await queue_single.release()
        await asyncio.sleep(0.05)
        assert results == [0], f"Expected [0], got {results}"

        # Release again → waiter 1
        await queue_single.release()
        await asyncio.sleep(0.05)
        assert results == [0, 1], f"Expected [0, 1], got {results}"

        # Release again → waiter 2
        await queue_single.release()
        await asyncio.sleep(0.05)
        assert results == [0, 1, 2], f"Expected [0, 1, 2], got {results}"

        # All tasks should be done
        for t in tasks:
            assert t.done(), f"Task not done: {t}"

    async def test_concurrent_slot_reuse(self, queue: SlottedQueue) -> None:
        """Multiple slots free up concurrently — waiters get assigned fairly."""
        # Fill all 3 slots
        acquired = [await queue.acquire() for _ in range(3)]

        # Enqueue 3 waiters
        results: list[int] = []
        order: list[int] = []

        async def waiter(n: int) -> None:
            ticket = await queue.acquire(timeout=5.0)
            order.append(n)
            results.append(n)
            await asyncio.sleep(0.05)
            await queue.release()

        tasks = [asyncio.create_task(waiter(i)) for i in range(3)]
        await asyncio.sleep(0.05)
        assert queue.queued == 3

        # Release all 3 original slots at once
        for t in acquired:
            await queue.release()

        await asyncio.sleep(0.2)
        # All 3 waiters should have been processed in FIFO order
        assert order == [0, 1, 2], f"Expected FIFO [0,1,2], got {order}"
        assert len(results) == 3

        for t in tasks:
            assert t.done()


# ===========================================================================
# Timeout behaviour
# ===========================================================================


class TestTimeout:
    """Verify that queued requests expire and return 503."""

    async def test_timeout_when_queue_full(self, queue: SlottedQueue) -> None:
        """A request that waits too long gets an invalid ticket."""
        t1 = await queue.acquire()
        t2 = await queue.acquire()
        t3 = await queue.acquire()
        assert queue.active == 3

        # Fourth caller should time out (queue_timeout=1s)
        start = time.monotonic()
        ticket = await queue.acquire(timeout=0.5)
        elapsed = time.monotonic() - start

        assert not ticket.valid, "Expected invalid ticket on timeout"
        assert ticket.reason == "request timed out in queue"
        assert 0.4 <= elapsed < 2.0, f"Timeout should fire near 0.5s (took {elapsed:.2f}s)"

    async def test_timeout_watcher_cleans_expired_requests(self, queue: SlottedQueue) -> None:
        """The background watcher removes expired requests without leaking."""
        # Fill all 3 slots
        await queue.acquire()
        await queue.acquire()
        await queue.acquire()

        # Enqueue 2 waiters with long default timeout
        t4 = asyncio.create_task(queue.acquire(timeout=5.0))  # won't expire by default
        t5 = asyncio.create_task(queue.acquire(timeout=0.5))  # will expire quickly

        await asyncio.sleep(0.1)
        assert queue.queued == 2

        # Wait for the short-timeout one to expire
        await asyncio.sleep(0.6)
        ticket5 = await t5
        assert not ticket5.valid, "Short-timeout request should have expired"

        # Only 1 should remain in queue
        assert queue.queued == 1, f"Expected 1 queued after expiry, got {queue.queued}"

        # The long-timeout one should still be waiting
        assert not t4.done(), "Long-timeout request should still be waiting"

        # Free a slot → t4 gets it
        await queue.release()
        ticket4 = await asyncio.wait_for(t4, timeout=1.0)
        assert ticket4.valid, "Long-timeout request should get the slot"

    async def test_timeout_watcher_removes_front_expired_first(self, queue: SlottedQueue) -> None:
        """The watcher scans from the front (oldest first), respecting FIFO."""
        # Fill all 3 slots
        await queue.acquire()
        await queue.acquire()
        await queue.acquire()

        # Enqueue waiters with staggered timeouts
        results: list[tuple[str, bool]] = []

        async def waiter(label: str, timeout: float) -> None:
            ticket = await queue.acquire(timeout=timeout)
            results.append((label, ticket.valid))
            if ticket.valid:
                await queue.release()

        t_short = asyncio.create_task(waiter("A-short-0.5", 0.5))
        await asyncio.sleep(0.1)
        t_medium = asyncio.create_task(waiter("B-medium-1.2", 1.2))
        await asyncio.sleep(0.1)
        t_long = asyncio.create_task(waiter("C-long-2.0", 2.0))
        await asyncio.sleep(0.1)

        assert queue.queued == 3

        # Wait for A to expire (0.5s)
        await asyncio.sleep(0.6)
        await t_short
        assert results[-1] == ("A-short-0.5", False), "A should have expired"

        # Release one slot — B should get it (FIFO: B is now front)
        await queue.release()
        await asyncio.sleep(0.5)
        assert ("B-medium-1.2", True) in results, "B should have gotten the slot"

    async def test_no_memory_leak_from_expired(self, queue: SlottedQueue) -> None:
        """Expired requests are removed from the queue and their futures resolved."""
        # Fill all slots
        for _ in range(3):
            await queue.acquire()

        # Enqueue 5 and let them expire
        waiters = [asyncio.create_task(queue.acquire(timeout=0.3)) for _ in range(5)]
        await asyncio.sleep(0.6)

        for w in waiters:
            ticket = await w
            assert not ticket.valid

        assert queue.queued == 0, f"Queue should be empty after all expired, got {queue.queued}"
        assert queue.active == 3, "Active slots should be unaffected"

        # New request should work fine
        ticket = await queue.acquire(timeout=0.5)
        assert not ticket.valid, "Still 3 active, should time out"
        assert queue.queued == 0, "No queue buildup"

    async def test_timeout_on_enqueue_slot_quickly_available(self, queue: SlottedQueue) -> None:
        """Request that enqueues and gets a slot before timeout should succeed."""
        # Fill all 3 slots
        t1 = await queue.acquire()
        await queue.acquire()
        await queue.acquire()

        # Enqueue with long timeout
        t4_task = asyncio.create_task(queue.acquire(timeout=5.0))
        await asyncio.sleep(0.05)

        # Free a slot quickly
        await queue.release()
        ticket = await asyncio.wait_for(t4_task, timeout=1.0)
        assert ticket.valid, "Should have gotten the slot before timeout"

    async def test_immediate_slot_works_with_timeout(self, queue: SlottedQueue) -> None:
        """Fast path: acquire with timeout when slot is free returns immediately."""
        ticket = await queue.acquire(timeout=5.0)
        assert ticket.valid
        assert queue.active == 1


# ===========================================================================
# Edge cases & validation
# ===========================================================================


class TestValidation:
    """Verify constructor validation and edge cases."""

    def test_max_slots_rejects_zero(self) -> None:
        with pytest.raises(ValueError, match="max_slots must be >= 1"):
            SlottedQueue(max_slots=0)

    def test_max_slots_rejects_negative(self) -> None:
        with pytest.raises(ValueError, match="max_slots must be >= 1"):
            SlottedQueue(max_slots=-5)

    def test_queue_timeout_rejects_zero(self) -> None:
        with pytest.raises(ValueError, match="queue_timeout must be > 0"):
            SlottedQueue(max_slots=1, queue_timeout=0)

    def test_queue_timeout_rejects_negative(self) -> None:
        with pytest.raises(ValueError, match="queue_timeout must be > 0"):
            SlottedQueue(max_slots=1, queue_timeout=-1)

    def test_state_on_construction(self) -> None:
        q = SlottedQueue(max_slots=5, queue_timeout=10.0)
        assert q.max_slots == 5
        assert q.queue_timeout == 10.0
        assert q.active == 0
        assert q.queued == 0
        assert q.available_slots == 5
        assert q.total_in_flight == 0


class TestQueueProperties:
    """Verify property reporting."""

    async def test_available_slots(self, queue: SlottedQueue) -> None:
        assert queue.available_slots == 3
        await queue.acquire()
        assert queue.available_slots == 2
        await queue.acquire()
        assert queue.available_slots == 1
        await queue.acquire()
        assert queue.available_slots == 0

    async def test_total_in_flight(self, queue: SlottedQueue) -> None:
        assert queue.total_in_flight == 0
        t1 = await queue.acquire()
        assert queue.total_in_flight == 1
        # Enqueue one
        task = asyncio.create_task(queue.acquire(timeout=5.0))
        await asyncio.sleep(0.05)
        # With 3 slots, the 4th call's first one should... wait, max_slots=3
        # So after 3 acquires, the 4th queues
        await queue.acquire()
        await queue.acquire()
        task2 = asyncio.create_task(queue.acquire(timeout=5.0))
        await asyncio.sleep(0.05)
        assert queue.total_in_flight == 4  # 3 active + 1 queued


# ===========================================================================
# QueueRequest internals
# ===========================================================================


class TestQueueRequest:
    """Verify the internal request dataclass."""

    def test_request_id_is_unique(self) -> None:
        ids = {QueueRequest().request_id for _ in range(100)}
        assert len(ids) == 100, "Request IDs should be unique"

    def test_enqueued_at_is_monotonic(self) -> None:
        r1 = QueueRequest()
        r2 = QueueRequest()
        assert r2.enqueued_at >= r1.enqueued_at

    def test_future_is_none_on_creation(self) -> None:
        """Future is None by default (created lazily in acquire())."""
        req = QueueRequest()
        assert req.future is None

    def test_defaults(self) -> None:
        req = QueueRequest()
        assert isinstance(req.request_id, str)
        assert len(req.request_id) > 0
        assert req.enqueued_at > 0
        assert req.future is None


# ===========================================================================
# Stress / concurrency
# ===========================================================================


class TestStress:
    """Heavier concurrent tests to ensure correctness under load."""

    async def test_stress_round_robin(self) -> None:
        """50 requests through a 5-slot queue — all processed, no deadlocks."""
        q = SlottedQueue(max_slots=5, queue_timeout=10.0)
        await q.start()

        async def worker(n: int) -> int:
            ticket = await q.acquire(timeout=5.0)
            if not ticket.valid:
                return -1
            await asyncio.sleep(0.01)  # simulate work
            await q.release()
            return n

        tasks = [asyncio.create_task(worker(i)) for i in range(50)]
        results = await asyncio.gather(*tasks)
        await q.stop()

        assert len(results) == 50
        assert all(r >= 0 for r in results), f"Some requests failed: {[r for r in results if r < 0]}"
        assert q.active == 0, f"Active slots leftover: {q.active}"
        assert q.queued == 0, f"Queue not drained: {q.queued}"

    async def test_stress_with_some_timeouts(self) -> None:
        """Mix of quick and slow requests — expired ones don't block progress."""
        q = SlottedQueue(max_slots=2, queue_timeout=2.0)
        await q.start()

        # Hold both slots
        t1 = await q.acquire()
        t2 = await q.acquire()

        slow_results: list[int] = []

        async def slow_worker(n: int) -> int:
            ticket = await q.acquire(timeout=4.0)
            if ticket.valid:
                await asyncio.sleep(0.3)
                await q.release()
                slow_results.append(n)
                return n
            return -1

        async def fast_worker(n: int) -> int:
            ticket = await q.acquire(timeout=0.2)  # will expire fast
            if not ticket.valid:
                return -1
            await asyncio.sleep(0.01)
            await q.release()
            return n

        # Enqueue mix
        tasks = []
        for i in range(10):
            tasks.append(asyncio.create_task(slow_worker(i)))
        # Also enqueue some that should time out
        fast_tasks = [asyncio.create_task(fast_worker(100 + i)) for i in range(5)]

        await asyncio.sleep(0.3)  # fast ones expire

        # Free both slots → the 2 slowest waiters get them
        await q.release()
        await q.release()

        await asyncio.sleep(0.6)  # slow workers run

        # Check fast ones timed out
        ft_results = await asyncio.gather(*fast_tasks)
        assert all(r == -1 for r in ft_results), "Fast workers should have all timed out"

        # Remaining slow workers get processed sequentially
        for _ in range(10):
            await asyncio.sleep(0.05)
            if q.active == 0 and q.queued == 0:
                break
            # Free next slot
            await q.release()

        await q.stop()
        assert q.active == 0
        assert q.queued == 0
        assert len(slow_results) > 0, "At least some slow workers should have completed"
