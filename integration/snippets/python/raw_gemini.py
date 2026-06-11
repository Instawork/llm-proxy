#!/usr/bin/env python3
import os
import sys

import httpx

base_url = os.environ.get("PROXY_BASE_URL")
api_key = os.environ.get("PROXY_API_KEY")
if not base_url or not api_key:
    print("PROXY_BASE_URL and PROXY_API_KEY required", file=sys.stderr)
    sys.exit(1)

resp = httpx.post(
    f"{base_url.rstrip('/')}/v1beta/models/gemini-2.5-flash:generateContent",
    headers={"x-goog-api-key": api_key},
    json={"contents": [{"parts": [{"text": "Hello from the proxy!"}]}]},
    timeout=60.0,
)
resp.raise_for_status()
data = resp.json()
print(data["candidates"][0]["content"]["parts"][0]["text"] or "(empty)")
