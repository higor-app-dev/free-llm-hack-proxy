#!/usr/bin/env bash
# Start server with test key
set -euo pipefail
cd /home/higor/free-llm-hack-proxy

export LLM_PROXY_API_KEY='sk-test-key-12345'
export LLM_PROXY_SERVER__PORT='18084'
export LLM_PROXY_RATE_LIMIT__ENABLED='false'

exec python3 -m uvicorn src.proxy:app --host 127.0.0.1 --port 18084 --log-level info
