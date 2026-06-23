"""Tests for ``src.proxy.rate_limiter`` — sliding window rate limiter.

Covers:
  1. Unit: basic allow / deny lifecycle
  2. Unit: sliding window expiration
  3. Unit: edge cases (zero limit, concurrent calls)
  4. Integration: rate limit enforcement on /v1/chat/completions (429 + Retry-After)
  5. Integration: no interference with /health or /v1/queue/status
  6. Integration: disabled rate limiter passes all requests
"""

from __future__ import annotations

import asyncio
from unittest.mock import patch

import pytest
from fastapi.testclient import TestClient

from src.config import reset_config
from src.proxy.rate_limiter import RateLimitExceeded, SlidingWindowRateLimiter


# ===========================================================================
# Unit tests — SlidingWindowRateLimiter
# ===========================================================================


class TestSlidingWindowRateLimiter:
    """Pure unit tests for the rate limiter class (no server needed)."""

    async def test_allow_first_request(self) -> None:
        """First request is always allowed."""
        limiter = SlidingWindowRateLimiter(max_requests=3, window_seconds=60)
        result = await limiter.check()
        assert result is None

    async def test_allow_within_limit(self) -> None:
        """Requests under the limit are allowed."""
        limiter = SlidingWindowRateLimiter(max_requests=3, window_seconds=60)
        for _ in range(3):
            result = await limiter.check()
            assert result is None

    async def test_exceed_limit_raises_exception(self) -> None:
        """The 4th request (limit=3) raises RateLimitExceeded."""
        limiter = SlidingWindowRateLimiter(max_requests=3, window_seconds=60)
        for _ in range(3):
            await limiter.check()

        with pytest.raises(RateLimitExceeded) as exc_info:
            await limiter.check()

        assert exc_info.value.allowed == 3
        assert exc_info.value.retry_after >= 1

    async def test_retry_after_provided_in_header_value(self) -> None:
        """The retry_after value is the time until the oldest entry expires."""
        limiter = SlidingWindowRateLimiter(max_requests=2, window_seconds=10)
        await limiter.check()
        await limiter.check()

        with pytest.raises(RateLimitExceeded) as exc_info:
            await limiter.check()

        # The oldest entry expires in <10 seconds, so retry_after is ≤ window
        assert 1 <= exc_info.value.retry_after <= 10

    async def test_window_expires_requests_are_allowed_again(self) -> None:
        """After the window passes, new requests are allowed."""
        limiter = SlidingWindowRateLimiter(max_requests=2, window_seconds=0.05)
        await limiter.check()
        await limiter.check()

        with pytest.raises(RateLimitExceeded):
            await limiter.check()

        # Wait for the window to pass
        await asyncio.sleep(0.06)

        # Now the oldest entries have expired — should be allowed
        result = await limiter.check()
        assert result is None

    async def test_current_count_and_remaining(self) -> None:
        """current_count and remaining reflect state correctly."""
        limiter = SlidingWindowRateLimiter(max_requests=5, window_seconds=60)
        assert limiter.current_count == 0
        assert limiter.remaining == 5

        await limiter.check()
        assert limiter.current_count == 1
        assert limiter.remaining == 4

        await limiter.check()
        assert limiter.current_count == 2
        assert limiter.remaining == 3

    async def test_oldest_timestamp_none_on_empty(self) -> None:
        """oldest_timestamp returns None when no requests have been recorded."""
        limiter = SlidingWindowRateLimiter(max_requests=5, window_seconds=60)
        assert limiter.oldest_timestamp is None

    async def test_oldest_timestamp_after_request(self) -> None:
        """oldest_timestamp returns a float after a request is recorded."""
        limiter = SlidingWindowRateLimiter(max_requests=5, window_seconds=60)
        await limiter.check()
        assert limiter.oldest_timestamp is not None
        assert isinstance(limiter.oldest_timestamp, float)

    async def test_reset_clears_state(self) -> None:
        """After reset(), the limiter behaves as if new."""
        limiter = SlidingWindowRateLimiter(max_requests=2, window_seconds=60)
        await limiter.check()
        await limiter.check()
        assert limiter.current_count == 2

        await limiter.reset()
        assert limiter.current_count == 0
        assert limiter.oldest_timestamp is None

        # Should be allowed again
        result = await limiter.check()
        assert result is None

    async def test_invalid_parameters_raise_value_error(self) -> None:
        """max_requests < 1 and window <= 0 raise ValueError."""
        with pytest.raises(ValueError, match="max_requests must be >= 1"):
            SlidingWindowRateLimiter(max_requests=0, window_seconds=60)
        with pytest.raises(ValueError, match="window_seconds must be > 0"):
            SlidingWindowRateLimiter(max_requests=5, window_seconds=0)

    async def test_concurrent_calls_are_thread_safe(self) -> None:
        """Multiple concurrent coroutines do not corrupt the timestamp list."""
        limiter = SlidingWindowRateLimiter(max_requests=5, window_seconds=60)

        async def hammer(n: int) -> None:
            for _ in range(n):
                try:
                    await limiter.check()
                except RateLimitExceeded:
                    pass

        # Launch 10 concurrent tasks, each trying 3 times (30 total, limit 5)
        await asyncio.gather(*(hammer(3) for _ in range(10)))

        # At most 5 timestamps should exist (the ones that got through)
        assert limiter.current_count <= 5

    async def test_rate_limit_exceeded_string_representation(self) -> None:
        """The exception string should describe the limit."""
        exc = RateLimitExceeded(retry_after=5.0, allowed=10, window=60.0)
        msg = str(exc)
        assert "10" in msg
        assert "60" in msg
        assert "5" in msg

    async def test_disabled_when_max_requests_large(self) -> None:
        """A very large max_requests effectively disables the limiter."""
        limiter = SlidingWindowRateLimiter(max_requests=1_000_000, window_seconds=1)
        for _ in range(100):
            result = await limiter.check()
            assert result is None


# ===========================================================================
# Integration tests — rate limiter wired into FastAPI
# ===========================================================================


def _make_chat_request(client: TestClient):
    """Helper: POST /v1/chat/completions with a minimal payload."""
    return client.post(
        "/v1/chat/completions",
        json={
            "model": "test-model",
            "messages": [{"role": "user", "content": "hello"}],
        },
    )


class TestRateLimitEnforcementLow:
    """Integration tests with a low rate limit (3 req / 60s).

    Each test uses its own env-vars + fresh reset + TestClient lifespan
    so the rate limiter picks up the test-specific config.
    """

    LOW_LIMIT = {"LLM_PROXY_RATE_LIMIT__MAX_REQUESTS": "3", "LLM_PROXY_RATE_LIMIT__WINDOW_SECONDS": "60"}

    def test_chat_completions_allowed_within_limit(self) -> None:
        """Requests under the rate limit return 200."""
        with patch.dict("os.environ", self.LOW_LIMIT):
            reset_config()
            with TestClient(imported_app) as client:
                for i in range(3):
                    resp = _make_chat_request(client)
                    assert resp.status_code == 200, (
                        f"Request {i + 1} should be 200, got {resp.status_code}: {resp.text}"
                    )

    def test_chat_completions_returns_429_when_exceeded(self) -> None:
        """The 4th request (limit=3) returns 429 with Retry-After."""
        with patch.dict("os.environ", self.LOW_LIMIT):
            reset_config()
            with TestClient(imported_app) as client:
                for _ in range(3):
                    resp = _make_chat_request(client)
                    assert resp.status_code == 200

                resp = _make_chat_request(client)
                assert resp.status_code == 429, f"Expected 429, got {resp.status_code}: {resp.text}"

                body = resp.json()
                assert body["error"]["type"] == "rate_limit_error"
                assert body["error"]["code"] == 429

                retry_after = resp.headers.get("Retry-After")
                assert retry_after is not None, "Missing Retry-After header"
                assert int(retry_after) >= 1

    def test_health_not_affected(self) -> None:
        """The /health endpoint returns 200 even after rate limit is exceeded."""
        with patch.dict("os.environ", self.LOW_LIMIT):
            reset_config()
            with TestClient(imported_app) as client:
                for _ in range(3):
                    _make_chat_request(client)

                # This one should be rate limited
                resp = _make_chat_request(client)
                assert resp.status_code == 429

                # /health works fine
                resp = client.get("/health")
                assert resp.status_code == 200
                assert resp.json() == {"status": "ok"}

    def test_queue_status_not_affected(self) -> None:
        """The /v1/queue/status endpoint returns 200 even after rate limit."""
        with patch.dict("os.environ", self.LOW_LIMIT):
            reset_config()
            with TestClient(imported_app) as client:
                for _ in range(3):
                    _make_chat_request(client)

                # /v1/queue/status works regardless
                resp = client.get("/v1/queue/status")
                assert resp.status_code == 200
                data = resp.json()
                assert "max_slots" in data
                assert "active" in data

    def test_429_response_has_correct_error_shape(self) -> None:
        """The 429 error body matches the OpenAI error format."""
        with patch.dict("os.environ", self.LOW_LIMIT):
            reset_config()
            with TestClient(imported_app) as client:
                for _ in range(3):
                    _make_chat_request(client)

                resp = _make_chat_request(client)
                body = resp.json()

                assert "error" in body
                error = body["error"]
                assert error["type"] == "rate_limit_error"
                assert error["code"] == 429
                assert "message" in error
                assert "rate limit exceeded" in error["message"].lower()


class TestRateLimitEnforcementExtreme:
    """Tests with extremely tight rate limit (1 req / 3600s)."""

    STRICT = {"LLM_PROXY_RATE_LIMIT__MAX_REQUESTS": "1", "LLM_PROXY_RATE_LIMIT__WINDOW_SECONDS": "3600"}

    def test_single_request_allowed_then_blocked(self) -> None:
        """1st request succeeds, 2nd is blocked."""
        with patch.dict("os.environ", self.STRICT):
            reset_config()
            with TestClient(imported_app) as client:
                resp = _make_chat_request(client)
                assert resp.status_code == 200

                resp = _make_chat_request(client)
                assert resp.status_code == 429


class TestRateLimiterDisabled:
    """When rate limiting is disabled, all requests pass."""

    DISABLED = {
        "LLM_PROXY_RATE_LIMIT__ENABLED": "false",
        "LLM_PROXY_RATE_LIMIT__MAX_REQUESTS": "1",
        "LLM_PROXY_RATE_LIMIT__WINDOW_SECONDS": "60",
    }

    def test_allows_all_requests(self) -> None:
        """Even with max_requests=1, disabling allows all."""
        with patch.dict("os.environ", self.DISABLED):
            reset_config()
            with TestClient(imported_app) as client:
                for _ in range(10):
                    resp = _make_chat_request(client)
                    assert resp.status_code == 200, (
                        f"Expected 200 (disabled), got {resp.status_code}: {resp.text}"
                    )


class TestRateLimiterConfigOverride:
    """Verify that env-var config overrides work correctly."""

    def test_max_requests_override(self) -> None:
        """LLM_PROXY_RATE_LIMIT__MAX_REQUESTS is picked up."""
        with patch.dict("os.environ", {"LLM_PROXY_RATE_LIMIT__MAX_REQUESTS": "42"}):
            reset_config()
            from src.config import load_config

            cfg = load_config(reload=True)
            assert cfg.rate_limit.max_requests == 42
            assert cfg.rate_limit.window_seconds == 60  # default

    def test_window_seconds_override(self) -> None:
        """LLM_PROXY_RATE_LIMIT__WINDOW_SECONDS is picked up."""
        with patch.dict("os.environ", {"LLM_PROXY_RATE_LIMIT__WINDOW_SECONDS": "10"}):
            reset_config()
            from src.config import load_config

            cfg = load_config(reload=True)
            assert cfg.rate_limit.window_seconds == 10

    def test_disabled_override(self) -> None:
        """LLM_PROXY_RATE_LIMIT__ENABLED=false disables the limiter."""
        with patch.dict("os.environ", {"LLM_PROXY_RATE_LIMIT__ENABLED": "false"}):
            reset_config()
            from src.config import load_config

            cfg = load_config(reload=True)
            assert cfg.rate_limit.enabled is False


# ---------------------------------------------------------------------------
# Import the app AFTER config overrides so patching "os.environ" works
# ---------------------------------------------------------------------------
# The app module reads config at import time via the lifespan. By delaying
# the import to module level AFTER defining the test classes, the fixture
# and test methods can control env vars before each TestClient lifespan fires.
from src.proxy import app as imported_app  # noqa: E402
