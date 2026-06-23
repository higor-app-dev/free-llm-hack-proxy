#!/usr/bin/env python3
"""Proxy server for the free-llm-hack-proxy.

Provides a FastAPI application with:
  - ``GET /health`` — returns ``{"status":"ok"}`` for Docker HEALTHCHECK and monitoring
  - ``GET /v1/queue/status`` — current queue depth, active slots, queue stats
  - ``POST /v1/chat/completions`` — OpenAI-compatible chat endpoint with global rate
    limiter (configurable via ``rate_limit.*``) and slot-managed FIFO queue.
  - ``start_server()`` — entry point for the CLI ``start`` command

The FIFO queue lifecycle is bound to the FastAPI lifespan: on startup the
queue is initialised with ``max_slots`` and ``queue_timeout`` from the
global config; on shutdown it is gracefully stopped.
"""

from __future__ import annotations

import logging
import time
from contextlib import asynccontextmanager
from typing import AsyncGenerator

import uvicorn
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, Response

from src.config import get_config
from src.proxy.metrics import (
    METRICS_CONTENT_TYPE,
    active_requests,
    metrics_output,
    request_wait_seconds,
)
from src.proxy.queue import SlottedQueue
from src.proxy.rate_limiter import RateLimitExceeded, SlidingWindowRateLimiter

logger = logging.getLogger("proxy")

# ---------------------------------------------------------------------------
# Global instances (initialised during lifespan startup)
# ---------------------------------------------------------------------------

_queue: SlottedQueue | None = None
_rate_limiter: SlidingWindowRateLimiter | None = None


def get_queue() -> SlottedQueue:
    """Return the global queue instance.

    Raises ``RuntimeError`` if called before the app has started (i.e.
    before the lifespan has initialised the queue).
    """
    if _queue is None:
        raise RuntimeError("Queue not initialised — app not started yet")
    return _queue


def get_rate_limiter() -> SlidingWindowRateLimiter:
    """Return the global rate limiter instance.

    Raises ``RuntimeError`` if called before the app has started (i.e.
    before the lifespan has initialised the limiter).
    """
    if _rate_limiter is None:
        raise RuntimeError("Rate limiter not initialised — app not started yet")
    return _rate_limiter


# ---------------------------------------------------------------------------
# Lifespan — wires up the queue lifecycle
# ---------------------------------------------------------------------------


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncGenerator[None, None]:
    """FastAPI lifespan: initialise queue + rate limiter on startup, tear down on shutdown."""
    global _queue, _rate_limiter

    cfg = get_config()
    _queue = SlottedQueue(
        max_slots=cfg.server.max_slots,
        queue_timeout=float(cfg.server.queue_timeout),
    )
    await _queue.start()
    logger.info(
        "Queue initialised (max_slots=%s, queue_timeout=%ss)",
        cfg.server.max_slots,
        cfg.server.queue_timeout,
    )

    # Initialise rate limiter (always create; the dependency checks enabled)
    _rate_limiter = SlidingWindowRateLimiter(
        max_requests=cfg.rate_limit.max_requests,
        window_seconds=float(cfg.rate_limit.window_seconds),
    )
    if cfg.rate_limit.enabled:
        logger.info(
            "Rate limiter enabled (max_requests=%s, window=%ss)",
            cfg.rate_limit.max_requests,
            cfg.rate_limit.window_seconds,
        )
    else:
        logger.info("Rate limiter disabled via config")

    yield  # app is running here

    await _queue.stop()
    _queue = None
    _rate_limiter = None
    logger.info("Queue stopped")


# ---------------------------------------------------------------------------
# FastAPI application
# ---------------------------------------------------------------------------

app = FastAPI(
    title="Free LLM Hack Proxy",
    version="0.1.0",
    description="Proxy for free LLM access via public APIs",
    lifespan=lifespan,
)


# ---------------------------------------------------------------------------
# Endpoints
# ---------------------------------------------------------------------------


@app.get("/health")
async def health() -> JSONResponse:
    """Healthcheck endpoint for Docker HEALTHCHECK and monitoring.

    Always returns ``200 OK`` with ``{"status":"ok"}``.
    No authentication required.
    """
    return JSONResponse(content={"status": "ok"})


@app.get("/v1/queue/status")
async def queue_status() -> JSONResponse:
    """Return current queue statistics.

    Returns
    -------
    JSONResponse
        ``{"active": N, "queued": M, "max_slots": K, "available_slots": P}``
    """
    q = get_queue()
    return JSONResponse(
        content={
            "active": q.active,
            "queued": q.queued,
            "max_slots": q.max_slots,
            "available_slots": q.available_slots,
            "total_in_flight": q.total_in_flight,
        }
    )


@app.get("/metrics")
async def metrics_endpoint() -> Response:
    """Prometheus metrics endpoint.

    Exposes all registered metrics in Prometheus exposition format.
    Used by Prometheus scrapers and ``curl localhost:8080/metrics`` for
    manual inspection.
    """
    return Response(
        content=metrics_output(),
        media_type=METRICS_CONTENT_TYPE,
    )


@app.post("/v1/chat/completions")
async def chat_completions(request: Request) -> JSONResponse:
    """OpenAI-compatible chat completions endpoint.

    Applies a global rate limiter first (configurable via
    ``rate_limit.*`` settings).  If the limit is exceeded, a 429
    response is returned with a ``Retry-After`` header.

    Then uses the slot-managed FIFO queue: if all slots are full, the
    request is enqueued.  If it waits longer than ``queue_timeout``
    seconds, a 503 is returned.
    """
    # --- Rate limit check ---
    cfg = get_config()
    if cfg.rate_limit.enabled:
        try:
            await get_rate_limiter().check()
        except RateLimitExceeded as exc:
            return JSONResponse(
                status_code=429,
                content={
                    "error": {
                        "message": str(exc),
                        "type": "rate_limit_error",
                        "code": 429,
                    }
                },
                headers={"Retry-After": str(exc.retry_after)},
            )

    # -- Metrics: start timing queue wait --
    wait_start = time.monotonic()
    ticket = await get_queue().acquire()

    if not ticket.valid:
        return JSONResponse(
            status_code=503,
            content={
                "error": {
                    "message": ticket.reason,
                    "type": "queue_timeout",
                    "code": 503,
                }
            },
        )

    # -- Metrics: record wait time and track active request --
    wait_seconds = time.monotonic() - wait_start
    route = "/v1/chat/completions"
    request_wait_seconds.labels(route=route, provider="unknown").observe(wait_seconds)
    active_requests.labels(route=route).inc()

    try:
        # --- Parse request body ---
        try:
            body = await request.json()
        except Exception:
            return JSONResponse(
                status_code=400,
                content={
                    "error": {
                        "message": "Invalid JSON body",
                        "type": "invalid_request_error",
                        "code": 400,
                    }
                },
            )

        model = body.get("model", "unknown")
        messages = body.get("messages", [])
        logger.info(
            "Processing request %s (model=%s, messages=%s)",
            ticket.request_id,
            model,
            len(messages),
        )

        # Re-record wait time with accurate provider label
        request_wait_seconds.labels(route=route, provider=model).observe(wait_seconds)

        # --- Placeholder: actual provider dispatch goes here ---
        # In the current implementation this returns a mock response.
        # When the provider router is integrated, replace this block.
        start = time.monotonic()
        await _simulate_provider_call(body)
        elapsed = time.monotonic() - start

        return JSONResponse(
            content={
                "id": f"chatcmpl-{ticket.request_id}",
                "object": "chat.completion",
                "model": model,
                "choices": [
                    {
                        "index": 0,
                        "message": {
                            "role": "assistant",
                            "content": f"Mock response for {model} "
                            f"(processed in {elapsed:.2f}s)",
                        },
                        "finish_reason": "stop",
                    }
                ],
                "usage": {
                    "prompt_tokens": 0,
                    "completion_tokens": 0,
                    "total_tokens": 0,
                },
            }
        )
    finally:
        await get_queue().release()
        active_requests.labels(route=route).dec()
        logger.debug("Released slot for request %s", ticket.request_id)


# ---------------------------------------------------------------------------
# Simulation helper (to be replaced with real provider dispatch)
# ---------------------------------------------------------------------------


async def _simulate_provider_call(body: dict) -> None:
    """Simulate a provider API call — sleeps briefly.

    This is a placeholder for the real provider router.  The sleep
    duration is proportional to the message count so tests can observe
    queue behaviour under load.
    """
    import asyncio

    delay = min(0.05 * len(body.get("messages", [])), 0.5)
    await asyncio.sleep(delay)


# ---------------------------------------------------------------------------
# Server entry point
# ---------------------------------------------------------------------------


def start_server(
    host: str = "0.0.0.0",
    port: int = 8080,
    workers: int = 1,
    reload: bool = False,
) -> None:
    """Launch the proxy server with uvicorn.

    Parameters
    ----------
    host : str
        Bind address (default: 0.0.0.0).
    port : int
        Listen port (default: 8080).
    workers : int
        Number of worker processes (default: 1).
    reload : bool
        Enable auto-reload on code changes (default: False).
    """
    logger.info(
        "Starting proxy server on %s:%s (workers=%s, reload=%s)",
        host,
        port,
        workers,
        reload,
    )

    uvicorn.run(
        "src.proxy:app",
        host=host,
        port=port,
        workers=workers,
        reload=reload,
        log_level="info",
    )
