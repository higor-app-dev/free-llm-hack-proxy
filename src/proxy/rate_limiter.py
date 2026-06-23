#!/usr/bin/env python3
"""Sliding-window rate limiter for the free-llm-hack-proxy.

Uses a sliding window log approach: each request timestamp is recorded.
When a new request arrives, timestamps older than the window are purged,
and if the remaining count is at the limit, the request is rejected with
a 429 status and a ``Retry-After`` header.

The limiter is **global** (not per-IP or per-key) — a single counter
protects all requests to the guarded endpoint.  This is intentional:
the goal is to protect the proxy itself from traffic spikes, not to
rate-limit individual downstream consumers.

Configuration
-------------
- ``max_requests`` : maximum number of requests allowed in each window
- ``window_seconds`` : width of the sliding window in seconds

Both are configurable via the ``RateLimitConfig`` section of the global
config (and via ``LLM_PROXY_RATE_LIMIT__*`` environment variables).

Thread safety
-------------
Designed for single-process async servers (uvicorn with 1 worker).
All operations hold an ``asyncio.Lock``.  For multi-worker deployments,
a shared backend (Redis, etc.) would be needed, but that's out of scope
for this implementation.

Usage::

    from src.proxy.rate_limiter import SlidingWindowRateLimiter, RateLimitExceeded

    limiter = SlidingWindowRateLimiter(max_requests=60, window_seconds=60)
    try:
        limiter.check()
    except RateLimitExceeded as exc:
        return JSONResponse(status_code=429, headers={"Retry-After": str(exc.retry_after)})
"""

from __future__ import annotations

import asyncio
import logging
import math
import time
from collections import deque

from src.proxy.metrics import rate_limited_total

logger = logging.getLogger("proxy.rate_limiter")


# ---------------------------------------------------------------------------
# Exception
# ---------------------------------------------------------------------------


class RateLimitExceeded(Exception):
    """Raised when a request exceeds the configured rate limit.

    Attributes
    ----------
    retry_after :
        Number of seconds the client should wait before retrying
        (fractional seconds rounded up to the nearest integer).
    allowed :
        The configured ``max_requests`` per window.
    window :
        The configured ``window_seconds``.
    """

    def __init__(
        self,
        retry_after: float,
        allowed: int,
        window: float,
    ) -> None:
        self.retry_after = math.ceil(retry_after) if retry_after > 0 else 1
        self.allowed = allowed
        self.window = window
        super().__init__(
            f"Rate limit exceeded: {allowed} requests per {window}s window. "
            f"Retry after {self.retry_after}s."
        )


# ---------------------------------------------------------------------------
# Sliding-window rate limiter
# ---------------------------------------------------------------------------


class SlidingWindowRateLimiter:
    """Global sliding-window rate limiter.

    Parameters
    ----------
    max_requests :
        Maximum number of requests allowed in any *window_seconds* sliding
        window.  Must be >= 1.
    window_seconds :
        Width of the sliding window in seconds.  Must be > 0.
    """

    def __init__(
        self,
        max_requests: int = 60,
        window_seconds: float = 60.0,
    ) -> None:
        if max_requests < 1:
            raise ValueError("max_requests must be >= 1")
        if window_seconds <= 0:
            raise ValueError("window_seconds must be > 0")

        self.max_requests = max_requests
        self.window_seconds = window_seconds

        self._lock = asyncio.Lock()
        # Sorted timestamps (monotonic) of recent requests — deque for O(1)
        # append/popleft on both ends.
        self._timestamps: deque[float] = deque()

    # ------------------------------------------------------------------
    # State inspection
    # ------------------------------------------------------------------

    @property
    def current_count(self) -> int:
        """Number of requests in the current window (best-effort, no lock)."""
        return len(self._timestamps)

    @property
    def remaining(self) -> int:
        """Remaining requests before hitting the limit (best-effort)."""
        return max(0, self.max_requests - self.current_count)

    @property
    def oldest_timestamp(self) -> float | None:
        """Oldest timestamp in the sliding window, or *None* if empty."""
        if not self._timestamps:
            return None
        return self._timestamps[0]

    # ------------------------------------------------------------------
    # Core check
    # ------------------------------------------------------------------

    async def check(self) -> float | None:
        """Check whether the current request is within the rate limit.

        If the request is allowed, its timestamp is recorded and
        *None* is returned.  If the limit has been exceeded, a
        :class:`RateLimitExceeded` exception is raised with the
        appropriate ``retry_after``.

        Returns
        -------
        float | None
            *None* if allowed.  (The exception path is the primary
            signalling mechanism for rejection.)
        """
        now = time.monotonic()
        cutoff = now - self.window_seconds

        async with self._lock:
            # Prune expired timestamps from the front (oldest) — the deque
            # is sorted by construction because we always append at the back.
            while self._timestamps and self._timestamps[0] < cutoff:
                self._timestamps.popleft()

            if len(self._timestamps) >= self.max_requests:
                # Reject: calculate how long until the oldest entry expires
                oldest = self._timestamps[0]
                retry_after = oldest + self.window_seconds - now
                logger.warning(
                    "Rate limit hit: %s requests in %.1fs window "
                    "(retry_after=%.1fs)",
                    self.max_requests,
                    self.window_seconds,
                    retry_after,
                )
                rate_limited_total.labels(route="/v1/chat/completions").inc()
                raise RateLimitExceeded(
                    retry_after=retry_after,
                    allowed=self.max_requests,
                    window=self.window_seconds,
                )

            # Allow
            self._timestamps.append(now)
            return None

    async def reset(self) -> None:
        """Clear all recorded timestamps — useful in tests."""
        async with self._lock:
            self._timestamps.clear()
