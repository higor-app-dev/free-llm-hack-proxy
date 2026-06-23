#!/usr/bin/env python3
"""Prometheus metrics for the free-llm-hack-proxy.

Defines all metric collectors in one place and exports helpers for the
``/metrics`` endpoint.  Metrics use the ``proxy_`` namespace prefix.

Usage
-----
Import the module to register the collectors::

    import src.proxy.metrics

Then update them from queue, rate limiter, and HTTP handler code::

    from src.proxy.metrics import (
        active_requests,
        queue_depth,
        rate_limited_total,
        request_wait_seconds,
    )

    queue_depth.labels(route="/v1/chat/completions").set(n)
    active_requests.labels(route="/v1/chat/completions").inc()
    rate_limited_total.labels(route="/v1/chat/completions").inc()
    request_wait_seconds.labels(route="/v1/chat/completions", provider=model).observe(t)
"""

from __future__ import annotations

from prometheus_client import CONTENT_TYPE_LATEST, Counter, Gauge, Histogram, generate_latest

# ===========================================================================
# Queue metrics
# ===========================================================================

queue_depth = Gauge(
    "proxy_queue_depth",
    "Current number of requests waiting in the FIFO queue",
    ["route"],
)

active_requests = Gauge(
    "proxy_active_requests",
    "Number of requests currently being processed (holding a slot)",
    ["route"],
)

request_wait_seconds = Histogram(
    "proxy_request_wait_seconds",
    "Time requests spent waiting in the queue (seconds)",
    ["route", "provider"],
    buckets=(
        0.001,
        0.005,
        0.01,
        0.025,
        0.05,
        0.1,
        0.25,
        0.5,
        1.0,
        2.5,
        5.0,
        10.0,
        30.0,
    ),
)

# ===========================================================================
# Rate limiter metrics
# ===========================================================================

rate_limited_total = Counter(
    "proxy_rate_limited_total",
    "Total number of requests rejected by the rate limiter (HTTP 429)",
    ["route"],
)

# ===========================================================================
# Helpers for the /metrics endpoint
# ===========================================================================

METRICS_CONTENT_TYPE = CONTENT_TYPE_LATEST


def metrics_output() -> bytes:
    """Return the full Prometheus exposition format for all registered metrics."""
    return generate_latest()
