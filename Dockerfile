# =============================================================================
# Multi-stage Dockerfile for free-llm-hack-proxy (Go)
#
# Stage 1 — builder: compile the Go binary
# Stage 2 — runtime: Alpine + Chromium + the binary
# =============================================================================

# ---------------------------------------------------------------------------
# Stage 1 — Go builder
# ---------------------------------------------------------------------------
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /build

# Cache dependencies
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true

# Build the binary
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /build/bin/free-llm-hack-proxy ./cmd/

# ---------------------------------------------------------------------------
# Stage 2 — runtime with Chromium
# ---------------------------------------------------------------------------
FROM alpine:3.19

LABEL org.opencontainers.image.title="free-llm-hack-proxy" \
      org.opencontainers.image.description="Proxy for free LLM access via public APIs" \
      org.opencontainers.image.source="https://github.com/higor/free-llm-hack-proxy" \
      org.opencontainers.image.version="0.1.0"

# ---- Install Chromium + runtime utils ----
RUN apk add --no-cache \
        chromium \
        curl \
        nss \
        freetype \
        harfbuzz \
        ca-certificates \
        tzdata \
    && true

# ---- Copy the Go binary ----
COPY --from=builder /build/bin/free-llm-hack-proxy /usr/local/bin/free-llm-hack-proxy

# ---- Runtime defaults ----
ENV HOST="0.0.0.0" \
    PORT="8080"

WORKDIR /app

EXPOSE 8080

# ---- Healthcheck ----
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD curl --fail http://localhost:8080/health || exit 1

CMD ["free-llm-hack-proxy"]
