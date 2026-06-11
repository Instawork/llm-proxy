#!/usr/bin/env python3
import os
import sys

os.environ.setdefault("OPENAI_BASE_URL", os.environ.get("PROXY_BASE_URL", ""))
os.environ.setdefault("OPENAI_API_KEY", os.environ.get("PROXY_API_KEY", ""))

if not os.environ["OPENAI_BASE_URL"] or not os.environ["OPENAI_API_KEY"]:
    print("PROXY_BASE_URL and PROXY_API_KEY required", file=sys.stderr)
    sys.exit(1)

from openai import OpenAI

client = OpenAI()
resp = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello from the proxy!"}],
)
print(resp.choices[0].message.content or "(empty)")
