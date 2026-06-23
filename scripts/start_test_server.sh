#!/usr/bin/env bash
# Start server with test key (Go version)
set -euo pipefail
cd "$(dirname "$0")/.."

export PORT='18084'
export LOG_LEVEL='debug'

exec go run ./cmd/
