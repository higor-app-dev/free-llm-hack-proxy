# =============================================================================
# Multi-stage Dockerfile for free-llm-hack-proxy
#
# Stage 1 — builder: install Python deps in a virtualenv
# Stage 2 — runtime: Alpine + Chromium + the app
# =============================================================================

# ---------------------------------------------------------------------------
# Stage 1 — Python dependency builder
# ---------------------------------------------------------------------------
FROM python:3.11-alpine AS builder

# Build-time deps for compiling native Python packages
RUN apk add --no-cache --virtual .build-deps \
        gcc \
        musl-dev \
        linux-headers \
        cargo \
    && true

WORKDIR /build

# Install project + all dependencies into a venv at /venv
COPY pyproject.toml README.md* ./
COPY src/ src/

RUN python -m venv /venv && \
    /venv/bin/pip install --no-cache-dir --upgrade pip setuptools wheel && \
    /venv/bin/pip install --no-cache-dir ".[all]" . && \
    # Remove build-deps to keep the venv layer clean (they're not needed at runtime)
    apk del .build-deps && \
    true

# ---------------------------------------------------------------------------
# Stage 2 — runtime with Chromium
# ---------------------------------------------------------------------------
FROM alpine:3.19

LABEL org.opencontainers.image.title="free-llm-hack-proxy" \
      org.opencontainers.image.description="Proxy for free LLM access via public APIs" \
      org.opencontainers.image.source="https://github.com/higor/free-llm-hack-proxy" \
      org.opencontainers.image.version="0.1.0"

# ---- Install Python + Chromium + curl ----
# Python is needed to run the venv (builder uses python:3.11-alpine, runtime
# must provide the same interpreter for the venv shebangs to resolve).
RUN apk add --no-cache \
        chromium \
        curl \
        python3 \
        # Chromium runtime deps
        nss \
        freetype \
        harfbuzz \
        ca-certificates \
        tzdata \
    && true

# ---- Copy the venv from the builder stage ----
COPY --from=builder /venv /venv

# Fix the python symlink — the venv's python points to /usr/local/bin/python
# (from the builder image), but alpine installs python3 at /usr/bin/python3.
RUN ln -sf /usr/bin/python3 /venv/bin/python && \
    # Strip .pyc to save ~5-10 MB
    find /venv -name '*.pyc' -delete && \
    find /venv -name '__pycache__' -type d -exec rm -rf {} + 2>/dev/null; \
    true

# ---- Copy the application source ----
COPY src/ /app/src/
COPY pyproject.toml /app/

# ---- Symlink the CLI entry point ----
RUN ln -s /venv/bin/llm-proxy /usr/local/bin/llm-proxy

# ---- Runtime defaults ----
ENV PATH="/venv/bin:${PATH}" \
    PYTHONUNBUFFERED=1 \
    LLM_PROXY_SERVER__HOST="0.0.0.0" \
    LLM_PROXY_SERVER__PORT="8080"

WORKDIR /app

EXPOSE 8080

# ---- Healthcheck ----
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD curl --fail http://localhost:8080/health || exit 1

# Default: start the proxy server via uvicorn directly
CMD ["uvicorn", "src.proxy:app", "--host", "0.0.0.0", "--port", "8080"]
