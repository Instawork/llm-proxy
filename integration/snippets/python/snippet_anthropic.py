#!/usr/bin/env python3
import os
import sys

from anthropic import Anthropic

base_url = os.environ.get("PROXY_BASE_URL")
api_key = os.environ.get("PROXY_API_KEY")
if not base_url or not api_key:
    print("PROXY_BASE_URL and PROXY_API_KEY required", file=sys.stderr)
    sys.exit(1)

client = Anthropic(base_url=base_url, api_key=api_key)
msg = client.messages.create(
    model="claude-sonnet-4-5",
    max_tokens=512,
    messages=[{"role": "user", "content": "Hello from the proxy!"}],
)
block = msg.content[0]
print(block.text if block.type == "text" else block)
