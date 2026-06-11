#!/usr/bin/env python3
import os
import sys

from google import genai
from google.genai import types

base_url = os.environ.get("PROXY_BASE_URL")
api_key = os.environ.get("PROXY_API_KEY")
if not base_url or not api_key:
    print("PROXY_BASE_URL and PROXY_API_KEY required", file=sys.stderr)
    sys.exit(1)

client = genai.Client(
    api_key=api_key,
    http_options=types.HttpOptions(base_url=base_url),
)
resp = client.models.generate_content(
    model="gemini-2.5-flash",
    contents="Hello from the proxy!",
)
print(resp.text or "(empty)")
