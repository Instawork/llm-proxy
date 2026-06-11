#!/usr/bin/env bash
set -euo pipefail

: "${PROXY_BASE_URL:?Set PROXY_BASE_URL e.g. http://localhost:9002/openai/v1}"
: "${PROXY_API_KEY:?Set PROXY_API_KEY to your iw: proxy key}"

curl -sfS "${PROXY_BASE_URL}/chat/completions" \
  -H "Authorization: Bearer ${PROXY_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Hello from the proxy!"}]
  }'

echo
