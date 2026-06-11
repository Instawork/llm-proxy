#!/usr/bin/env bash
set -euo pipefail

: "${PROXY_BASE_URL:?Set PROXY_BASE_URL e.g. http://localhost:9002/anthropic}"
: "${PROXY_API_KEY:?Set PROXY_API_KEY to your iw: proxy key}"

curl -sfS "${PROXY_BASE_URL}/v1/messages" \
  -H "x-api-key: ${PROXY_API_KEY}" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-5",
    "max_tokens": 512,
    "messages": [{"role": "user", "content": "Hello from the proxy!"}]
  }'

echo
