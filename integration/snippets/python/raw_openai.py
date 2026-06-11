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
    f"{base_url.rstrip('/')}/chat/completions",
    headers={"Authorization": f"Bearer {api_key}"},
    json={
        "model": "gpt-4o",
        "messages": [{"role": "user", "content": "Hello from the proxy!"}],
    },
    timeout=60.0,
)
resp.raise_for_status()
print(resp.json()["choices"][0]["message"]["content"] or "(empty)")
