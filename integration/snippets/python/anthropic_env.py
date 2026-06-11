#!/usr/bin/env python3
import os
import sys

os.environ.setdefault("ANTHROPIC_BASE_URL", os.environ.get("PROXY_BASE_URL", ""))
os.environ.setdefault("ANTHROPIC_API_KEY", os.environ.get("PROXY_API_KEY", ""))

if not os.environ["ANTHROPIC_BASE_URL"] or not os.environ["ANTHROPIC_API_KEY"]:
    print("PROXY_BASE_URL and PROXY_API_KEY required", file=sys.stderr)
    sys.exit(1)

from anthropic import Anthropic

client = Anthropic()
msg = client.messages.create(
    model="claude-sonnet-4-5",
    max_tokens=512,
    messages=[{"role": "user", "content": "Hello from the proxy!"}],
)
block = msg.content[0]
print(block.text if block.type == "text" else block)
