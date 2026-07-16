import type { Provider } from "../types";

export interface CodeExample {
  id: string;
  label: string;
  language: string;
  code: string;
}

interface Ctx {
  provider: Provider;
  baseUrl: string; // e.g. https://proxy/openai/v1
  key: string; // iw:... proxy key
}

function openai({ baseUrl, key }: Ctx): CodeExample[] {
  return [
    {
      id: "python",
      label: "Python",
      language: "python",
      code: `# pip install openai
from openai import OpenAI

client = OpenAI(
    base_url="${baseUrl}",
    api_key="${key}",  # your iw: proxy key
)

resp = client.chat.completions.create(
    model="gpt-4o",
    messages=[{"role": "user", "content": "Hello from the proxy!"}],
)
print(resp.choices[0].message.content)`,
    },
    {
      id: "node",
      label: "Node / TS",
      language: "typescript",
      code: `// npm i openai
import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "${baseUrl}",
  apiKey: "${key}", // your iw: proxy key
});

const resp = await client.chat.completions.create({
  model: "gpt-4o",
  messages: [{ role: "user", content: "Hello from the proxy!" }],
});
console.log(resp.choices[0].message.content);`,
    },
    {
      id: "go",
      label: "Go",
      language: "go",
      code: `// go get github.com/openai/openai-go
package main

import (
    "context"
    "fmt"

    "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
)

func main() {
    client := openai.NewClient(
        option.WithBaseURL("${baseUrl}"),
        option.WithAPIKey("${key}"), // your iw: proxy key
    )
    resp, _ := client.Chat.Completions.New(context.TODO(), openai.ChatCompletionNewParams{
        Model: openai.ChatModelGPT4o,
        Messages: []openai.ChatCompletionMessageParamUnion{
            openai.UserMessage("Hello from the proxy!"),
        },
    })
    fmt.Println(resp.Choices[0].Message.Content)
}`,
    },
    {
      id: "curl",
      label: "curl",
      language: "bash",
      code: `curl ${baseUrl}/chat/completions \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Hello from the proxy!"}]
  }'`,
    },
    {
      id: "http",
      label: "Raw HTTP",
      language: "python",
      code: `# pip install httpx
import httpx

resp = httpx.post(
    "${baseUrl}/chat/completions",
    headers={"Authorization": "Bearer ${key}"},
    json={
        "model": "gpt-4o",
        "messages": [{"role": "user", "content": "Hello from the proxy!"}],
    },
    timeout=60.0,
)
resp.raise_for_status()
print(resp.json()["choices"][0]["message"]["content"])`,
    },
    {
      id: "env",
      label: "Env vars",
      language: "bash",
      code: `# Most OpenAI-compatible tools (LangChain, Cursor, etc.) read these:
export OPENAI_BASE_URL="${baseUrl}"
export OPENAI_API_KEY="${key}"

# Then your existing code needs no changes:
#   client = OpenAI()   # picks up the env vars`,
    },
  ];
}

function anthropic({ baseUrl, key }: Ctx): CodeExample[] {
  return [
    {
      id: "python",
      label: "Python",
      language: "python",
      code: `# pip install anthropic
from anthropic import Anthropic

client = Anthropic(
    base_url="${baseUrl}",  # SDK appends /v1/messages
    api_key="${key}",        # your iw: proxy key
)

msg = client.messages.create(
    model="claude-sonnet-4-5",
    max_tokens=512,
    messages=[{"role": "user", "content": "Hello from the proxy!"}],
)
print(msg.content[0].text)`,
    },
    {
      id: "node",
      label: "Node / TS",
      language: "typescript",
      code: `// npm i @anthropic-ai/sdk
import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({
  baseURL: "${baseUrl}", // SDK appends /v1/messages
  apiKey: "${key}",       // your iw: proxy key
});

const msg = await client.messages.create({
  model: "claude-sonnet-4-5",
  max_tokens: 512,
  messages: [{ role: "user", content: "Hello from the proxy!" }],
});
console.log(msg.content[0].type === "text" ? msg.content[0].text : msg.content);`,
    },
    {
      id: "go",
      label: "Go",
      language: "go",
      code: `// go get github.com/anthropics/anthropic-sdk-go
package main

import (
    "context"
    "fmt"

    "github.com/anthropics/anthropic-sdk-go"
    "github.com/anthropics/anthropic-sdk-go/option"
)

func main() {
    client := anthropic.NewClient(
        option.WithBaseURL("${baseUrl}"),
        option.WithAPIKey("${key}"), // your iw: proxy key
    )
    msg, _ := client.Messages.New(context.TODO(), anthropic.MessageNewParams{
        Model:     anthropic.ModelClaudeSonnet4_5,
        MaxTokens: 512,
        Messages: []anthropic.MessageParam{
            anthropic.NewUserMessage(anthropic.NewTextBlock("Hello from the proxy!")),
        },
    })
    fmt.Println(msg.Content)
}`,
    },
    {
      id: "curl",
      label: "curl",
      language: "bash",
      code: `curl ${baseUrl}/v1/messages \\
  -H "x-api-key: ${key}" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "claude-sonnet-4-5",
    "max_tokens": 512,
    "messages": [{"role": "user", "content": "Hello from the proxy!"}]
  }'`,
    },
    {
      id: "http",
      label: "Raw HTTP",
      language: "python",
      code: `# pip install httpx
import httpx

resp = httpx.post(
    "${baseUrl}/v1/messages",
    headers={
        "x-api-key": "${key}",
        "anthropic-version": "2023-06-01",
    },
    json={
        "model": "claude-sonnet-4-5",
        "max_tokens": 512,
        "messages": [{"role": "user", "content": "Hello from the proxy!"}],
    },
    timeout=60.0,
)
resp.raise_for_status()
print(resp.json()["content"][0]["text"])`,
    },
    {
      id: "env",
      label: "Env vars",
      language: "bash",
      code: `# The Anthropic SDKs and Claude Code read these:
export ANTHROPIC_BASE_URL="${baseUrl}"
export ANTHROPIC_API_KEY="${key}"

# Existing code needs no changes:
#   client = Anthropic()   # picks up the env vars`,
    },
  ];
}

function gemini({ baseUrl, key }: Ctx): CodeExample[] {
  return [
    {
      id: "python",
      label: "Python",
      language: "python",
      code: `# pip install google-genai
from google import genai
from google.genai import types

client = genai.Client(
    api_key="${key}",  # your iw: proxy key
    http_options=types.HttpOptions(base_url="${baseUrl}"),
)

resp = client.models.generate_content(
    model="gemini-2.5-flash",
    contents="Hello from the proxy!",
)
print(resp.text)`,
    },
    {
      id: "node",
      label: "Node / TS",
      language: "typescript",
      code: `// npm i @google/genai
import { GoogleGenAI } from "@google/genai";

const ai = new GoogleGenAI({
  apiKey: "${key}", // your iw: proxy key
  httpOptions: { baseUrl: "${baseUrl}" },
});

const resp = await ai.models.generateContent({
  model: "gemini-2.5-flash",
  contents: "Hello from the proxy!",
});
console.log(resp.text);`,
    },
    {
      id: "go",
      label: "Go",
      language: "go",
      code: `// go get google.golang.org/genai
package main

import (
    "context"
    "fmt"

    "google.golang.org/genai"
)

func main() {
    ctx := context.Background()
    client, _ := genai.NewClient(ctx, &genai.ClientConfig{
        APIKey:      "${key}", // your iw: proxy key
        HTTPOptions: genai.HTTPOptions{BaseURL: "${baseUrl}"},
    })
    resp, _ := client.Models.GenerateContent(ctx, "gemini-2.5-flash",
        genai.Text("Hello from the proxy!"), nil)
    fmt.Println(resp.Text())
}`,
    },
    {
      id: "curl",
      label: "curl",
      language: "bash",
      code: `curl "${baseUrl}/v1beta/models/gemini-2.5-flash:generateContent" \\
  -H "x-goog-api-key: ${key}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "contents": [{"parts": [{"text": "Hello from the proxy!"}]}]
  }'`,
    },
    {
      id: "http",
      label: "Raw HTTP",
      language: "python",
      code: `# pip install httpx
import httpx

resp = httpx.post(
    "${baseUrl}/v1beta/models/gemini-2.5-flash:generateContent",
    headers={"x-goog-api-key": "${key}"},
    json={"contents": [{"parts": [{"text": "Hello from the proxy!"}]}]},
    timeout=60.0,
)
resp.raise_for_status()
data = resp.json()
print(data["candidates"][0]["content"]["parts"][0]["text"])`,
    },
  ];
}

export function codeExamples(ctx: Ctx): CodeExample[] {
  switch (ctx.provider) {
    case "openai":
      return openai(ctx);
    case "anthropic":
      return anthropic(ctx);
    case "gemini":
      return gemini(ctx);
    default:
      return openai(ctx);
  }
}

// Placeholder for the Cursor/Replit prompt — real key stays on the share page only.
export const PROMPT_KEY_PLACEHOLDER = "sk-iw-YOUR_PROXY_KEY_HERE";

const PROXY_KEY_IN_TEXT = /(?:sk-[a-z0-9]+-|iw[:_-])[a-f0-9]{32,}/gi;

/** Strip proxy keys from text meant for external AI assistants (Cursor, Replit, etc.). */
export function scrubProxyKeyFromText(text: string, knownKey?: string): string {
  let out = text;
  if (knownKey) {
    out = out.split(knownKey).join(PROMPT_KEY_PLACEHOLDER);
  }
  return out.replace(PROXY_KEY_IN_TEXT, PROMPT_KEY_PLACEHOLDER);
}

// assistantPrompt returns a ready-to-paste prompt for Cursor / Replit / other
// AI coding assistants to wire the proxy key into an existing project.
// Never pass the real key — use PROMPT_KEY_PLACEHOLDER only.
export function assistantPrompt({ provider, baseUrl }: Omit<Ctx, "key">): string {
  const envName =
    provider === "anthropic"
      ? "ANTHROPIC_BASE_URL / ANTHROPIC_API_KEY"
      : provider === "openai"
        ? "OPENAI_BASE_URL / OPENAI_API_KEY"
        : "the google-genai http_options baseUrl";

  const envKeyName =
    provider === "anthropic"
      ? "ANTHROPIC_API_KEY"
      : provider === "openai"
        ? "OPENAI_API_KEY"
        : "GEMINI_API_KEY (or your SDK's api key env var)";

  return `I'm using the LLM proxy instead of calling ${provider} directly. \
Please update my project to route all ${provider} calls through it.

Proxy base URL: ${baseUrl}
Proxy API key placeholder: ${PROMPT_KEY_PLACEHOLDER}

Important — the real proxy key is NOT in this prompt. Before I can run anything, tell me to open the \
LLM Proxy "How to use" (or share) page, copy my API key from the "Proxy API key" field, and paste it into ${envKeyName} \
(or replace ${PROMPT_KEY_PLACEHOLDER} wherever you wire the client). Do not invent or guess a key.

Requirements:
- Configure the ${provider} SDK to use the proxy base URL above (for OpenAI/Anthropic this is the \
${envName} setting; for Gemini set http_options.base_url / httpOptions.baseUrl).
- Use ${PROMPT_KEY_PLACEHOLDER} as a stand-in in code and .env.example until I paste the real key \
(Authorization: Bearer for OpenAI, x-api-key for Anthropic, x-goog-api-key for Gemini).
- Read the key from an environment variable rather than hardcoding it. Add it to .env and \
.env.example (with ${PROMPT_KEY_PLACEHOLDER} as the placeholder), and make sure .env is gitignored.
- Don't change my model names, prompts, or request logic — only the client base URL and key wiring.
- After wiring it up, add a tiny smoke-test script that sends one "Hello from the proxy!" message \
and prints the response so I can confirm it works once I've set the real key.`;
}
