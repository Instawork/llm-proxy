#!/usr/bin/env python3
import os
import sys

from openai import OpenAI

base_url = os.environ.get("PROXY_BASE_URL")
api_key = os.environ.get("PROXY_API_KEY")
if not base_url or not api_key:
    print("PROXY_BASE_URL and PROXY_API_KEY required", file=sys.stderr)
    sys.exit(1)

client = OpenAI(base_url=base_url, api_key=api_key)
resp = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello from the proxy!"}],
)
print(resp.choices[0].message.content or "(empty)")
