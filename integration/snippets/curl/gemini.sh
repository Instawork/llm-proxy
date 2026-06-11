#!/usr/bin/env bash
set -euo pipefail

: "${PROXY_BASE_URL:?Set PROXY_BASE_URL e.g. http://localhost:9002/gemini}"
: "${PROXY_API_KEY:?Set PROXY_API_KEY to your iw: proxy key}"

curl -sfS "${PROXY_BASE_URL}/v1beta/models/gemini-2.5-flash:generateContent" \
  -H "x-goog-api-key: ${PROXY_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"parts": [{"text": "Hello from the proxy!"}]}]
  }'

echo
