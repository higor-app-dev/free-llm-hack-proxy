#!/usr/bin/env python3
"""Integration smoke tests for the combined queue + rate limiter + metrics pipeline."""

from __future__ import annotations

import re
from unittest.mock import patch

import pytest
from fastapi.testclient import TestClient

from src.config import reset_config

# ── Helpers ─────────────────────────────────────────────────────────────────


def _chat_req(client: TestClient, model: str = "smoke-test") -> ...:
    return client.post(
        "/v1/chat/completions",
        json={"model": model, "messages": [{"role": "user", "content": "hello"}]},
    )


# Reset prometheus registries between tests
def _reset_metrics():
    """Re-initialize the prometheus registry by reloading the metrics module."""
    import src.proxy.metrics as m
    from prometheus_client import REGISTRY
    collectors = list(REGISTRY._collector_to_names.keys())
    for c in collectors:
        name = c.__class__.__name__
        if name != "MetricsCollector":  # don't remove built-ins like python_gc
            continue
        try:
            REGISTRY.unregister(c)
        except Exception:
            pass


# ── Tests ───────────────────────────────────────────────────────────────────


class TestQueueIntegration:
    """Queue works correctly with real HTTP requests."""

    def test_single_request(self):
        """A single chat completion returns 200 and valid response."""
        with patch.dict("os.environ", {}, clear=False):
            reset_config()
            with TestClient(imported_app) as client:
                resp = _chat_req(client)
                assert resp.status_code == 200, resp.text
                body = resp.json()
                assert body["object"] == "chat.completion"
                assert "choices" in body
                assert body["choices"][0]["message"]["role"] == "assistant"

    def test_queue_state_after_request(self):
        """After a request completes, queue shows idle state."""
        with patch.dict("os.environ", {}, clear=False):
            reset_config()
            with TestClient(imported_app) as client:
                resp = _chat_req(client)
                assert resp.status_code == 200

                status = client.get("/v1/queue/status").json()
                assert status["active"] == 0
                assert status["queued"] == 0
                assert status["max_slots"] == 10
                assert status["available_slots"] == 10
                assert status["total_in_flight"] == 0

    def test_queue_blocks_when_full(self):
        """When slots fill, requests queue and the status reflects it."""
        with patch.dict(
            "os.environ",
            {"LLM_PROXY_SERVER__MAX_SLOTS": "2"},
            clear=False,
        ):
            reset_config()
            with TestClient(imported_app) as client:
                # Fire 4 concurrent requests — only 2 fit, 2 queue
                import concurrent.futures
                with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
                    futures = [pool.submit(_chat_req, client) for _ in range(4)]
                    concurrent.futures.wait(futures)

                all_200 = all(f.result().status_code == 200 for f in futures)
                assert all_200, "All requests should eventually get a slot"

                status = client.get("/v1/queue/status").json()
                assert status["active"] == 0
                assert status["queued"] == 0


class TestRateLimitIntegration:
    """Rate limiter works correctly in the full HTTP pipeline."""

    def test_429_after_limit(self):
        """4th request (limit=3) returns 429 with correct error shape."""
        with patch.dict(
            "os.environ",
            {
                "LLM_PROXY_RATE_LIMIT__MAX_REQUESTS": "3",
                "LLM_PROXY_RATE_LIMIT__WINDOW_SECONDS": "60",
            },
            clear=False,
        ):
            reset_config()
            with TestClient(imported_app) as client:
                for i in range(3):
                    resp = _chat_req(client)
                    assert resp.status_code == 200, f"req {i}: {resp.text}"

                resp = _chat_req(client)
                assert resp.status_code == 429, f"expected 429: {resp.text}"
                body = resp.json()
                assert body["error"]["type"] == "rate_limit_error"
                assert body["error"]["code"] == 429
                assert "Retry-After" in resp.headers

    def test_disabled_rate_limiter_allows_all(self):
        """When rate_limit.enabled=false, all requests pass."""
        with patch.dict(
            "os.environ",
            {
                "LLM_PROXY_RATE_LIMIT__ENABLED": "false",
                "LLM_PROXY_RATE_LIMIT__MAX_REQUESTS": "1",
            },
            clear=False,
        ):
            reset_config()
            with TestClient(imported_app) as client:
                for _ in range(10):
                    resp = _chat_req(client)
                    assert resp.status_code == 200, f"expected 200: {resp.status_code}: {resp.text[:100]}"


class TestMetricsIntegration:
    """Metrics endpoint returns all expected metrics."""

    def test_metrics_contains_all_families(self):
        """All 4 metric families are present in /metrics output."""
        with patch.dict("os.environ", {}, clear=False):
            reset_config()
            with TestClient(imported_app) as client:
                resp = client.get("/metrics")
                assert resp.status_code == 200
                text = resp.text
                assert "# HELP proxy_queue_depth" in text
                assert "# HELP proxy_active_requests" in text
                assert "# HELP proxy_rate_limited_total" in text
                assert "# HELP proxy_request_wait_seconds" in text

    def test_metrics_show_after_a_request(self):
        """After a chat completion, metrics show data point(s)."""
        with patch.dict("os.environ", {}, clear=False):
            reset_config()
            with TestClient(imported_app) as client:
                _chat_req(client)

                resp = client.get("/metrics")
                text = resp.text
                # At least one observation for the known provider label
                assert 'proxy_request_wait_seconds_count{provider="smoke-test"' in text

    def test_rate_limited_metric_increments(self):
        """proxy_rate_limited_total > 0 when a request is rate-limited."""
        with patch.dict(
            "os.environ",
            {
                "LLM_PROXY_RATE_LIMIT__MAX_REQUESTS": "1",
                "LLM_PROXY_RATE_LIMIT__WINDOW_SECONDS": "60",
            },
            clear=False,
        ):
            reset_config()
            with TestClient(imported_app) as client:
                _chat_req(client)  # 200
                _chat_req(client)  # 429

                resp = client.get("/metrics")
                text = resp.text

                # Extract the raw counter value for proxy_rate_limited_total
                m = re.search(
                    r'proxy_rate_limited_total\{route="/v1/chat/completions"\}\s+([\d.]+)',
                    text,
                )
                assert m is not None, f"Could not find rate_limited_total metric in:\n{text}"
                value = float(m.group(1))
                assert value >= 1.0, f"Expected >=1 rate-limited request, got {value}"


class TestHealthAndStatusNotAffected:
    """/health and /v1/queue/status work even under load and rate limits."""

    def test_health_always_ok(self):
        """/health returns 200 even under heavy rate limiting."""
        with patch.dict(
            "os.environ",
            {
                "LLM_PROXY_RATE_LIMIT__MAX_REQUESTS": "1",
                "LLM_PROXY_RATE_LIMIT__WINDOW_SECONDS": "60",
            },
            clear=False,
        ):
            reset_config()
            with TestClient(imported_app) as client:
                _chat_req(client)
                _chat_req(client)  # 429

                resp = client.get("/health")
                assert resp.status_code == 200
                assert resp.json() == {"status": "ok"}

    def test_queue_status_works(self):
        """Queue status returns proper response."""
        with patch.dict("os.environ", {}, clear=False):
            reset_config()
            with TestClient(imported_app) as client:
                _chat_req(client)
                resp = client.get("/v1/queue/status")
                assert resp.status_code == 200
                data = resp.json()
                assert "active" in data
                assert "queued" in data
                assert "max_slots" in data
                assert "available_slots" in data
                assert "total_in_flight" in data


# Import app at module END so config patching works
from src.proxy import app as imported_app  # noqa: E402
