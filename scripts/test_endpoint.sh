#!/bin/bash
# Test the chat completions endpoint
set -euo pipefail

HOST="http://127.0.0.1:18085"
API_KEY="***"

echo "=== 1. No auth → 401 ==="
curl -s -w '\nHTTP_CODE:%{http_code}' "$HOST/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}],"stream":false}'
echo -e "\n"

echo "=== 2. Correct auth, non-streaming → 200 ==="
curl -s -w '\nHTTP_CODE:%{http_code}' "$HOST/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Olá mundo"}],"stream":false}'
echo -e "\n"

echo "=== 3. Correct auth, streaming → 200 SSE ==="
curl -s -w '\nHTTP_CODE:%{http_code}' "$HOST/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Teste de streaming"}],"stream":true}'
echo -e "\n"

echo "=== 4. Health (no auth) ==="
curl -s -o /dev/null -w 'HTTP:%{http_code}' "$HOST/health"
echo ""
