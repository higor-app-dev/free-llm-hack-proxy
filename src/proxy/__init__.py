#!/usr/bin/env python3
"""Proxy server for the free-llm-hack-proxy.

Provides a FastAPI application with:
  - ``GET /health`` — returns ``{"status":"ok"}`` for Docker HEALTHCHECK and monitoring
  - ``start_server()`` — entry point for the CLI ``start`` command
"""

from __future__ import annotations

import logging
import uvicorn
from fastapi import FastAPI
from fastapi.responses import JSONResponse

logger = logging.getLogger("proxy")

# ---------------------------------------------------------------------------
# FastAPI application
# ---------------------------------------------------------------------------

app = FastAPI(
    title="Free LLM Hack Proxy",
    version="0.1.0",
    description="Proxy for free LLM access via public APIs",
)


# ---------------------------------------------------------------------------
# Healthcheck endpoint (no authentication required)
# ---------------------------------------------------------------------------


@app.get("/health")
async def health() -> JSONResponse:
    """Healthcheck endpoint for Docker HEALTHCHECK and monitoring.

    Always returns ``200 OK`` with ``{"status":"ok"}``.
    No authentication required.
    """
    return JSONResponse(content={"status": "ok"})


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
