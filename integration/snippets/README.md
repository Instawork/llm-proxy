# Share-box snippet smoke tests

Runnable copies of the **Drop-in usage** examples from the admin share page (`web/src/lib/code-examples.ts`). Each script sends one `"Hello from the proxy!"` message through a running llm-proxy using an `iw:` proxy key.

Keep these in sync when you change the share UI snippets.

## Layout

```
snippets/
├── README.md           ← you are here
├── curl/               ← shell one-liners (no deps)
├── go/                 ← official Go SDKs (uses parent integration/go.mod)
├── node/               ← npm: openai, @anthropic-ai/sdk, @google/genai
└── python/             ← pip: openai, anthropic, google-genai (local .venv)
```

| Provider  | Base URL (SDK)              | Tabs in share UI              |
|-----------|-----------------------------|-------------------------------|
| OpenAI    | `{proxy}/openai/v1`         | Python, Node, Go, curl, Raw HTTP, Env   |
| Anthropic | `{proxy}/anthropic`         | Python, Node, Go, curl, Raw HTTP, Env   |
| Gemini    | `{proxy}/gemini`            | Python, Node, Go, curl, Raw HTTP        |

## Prerequisites

1. **llm-proxy running** (default `http://localhost:9002`):

   ```bash
   docker compose up -d
   ```

2. **Snippet dependencies** (auto-installed by `make test-live-snippets` or run once):

   ```bash
   make install-snippet-deps
   ```

   Installs `node_modules`, `python/.venv`, and runs `go mod download` in `integration/`.

3. **An `iw:` proxy key** for manual runs, or upstream keys for the automated runner (see below).

4. **Environment variables** (manual runs only):

   ```bash
   export PROXY_BASE_URL="http://localhost:9002/openai/v1"   # provider-specific
   export PROXY_API_KEY="iw:your-proxy-key"
   ```

## Manual runs

Use the venv Python for manual snippet runs:

```bash
PY=integration/snippets/python/.venv/bin/python3
export PROXY_BASE_URL="http://localhost:9002/openai/v1"
export PROXY_API_KEY="iw:..."

# curl
cd integration/snippets/curl && ./openai.sh

# Go (from integration/)
cd integration && go run ./snippets/go/openai/

# Node
cd integration/snippets/node && node openai.mjs

# Python
$PY integration/snippets/python/snippet_openai.py
$PY integration/snippets/python/openai_env.py
```

Provider base URLs: OpenAI `{proxy}/openai/v1`, Anthropic `{proxy}/anthropic`, Gemini `{proxy}/gemini`.

## Run everything (automated)

From repo root, with upstream keys and proxy up:

```bash
export OPENAI_API_KEY=sk-...
export ANTHROPIC_API_KEY=sk-ant-...
export GEMINI_API_KEY=...   # or GOOGLE_AI_API_KEY

make test-live-snippets
```

`make install-snippet-deps` runs first if needed. The live runner creates temporary `iw:` keys via the admin API, runs every share-box tab (curl, go, node, python, env), then deletes the keys.

## Env-tab snippets

OpenAI and Anthropic share an **Env vars** tab (`OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL`). Scripts `*_env.py` / `*-env.mjs` map `PROXY_*` into those env vars and use a default SDK client.

**Raw HTTP** tab uses `httpx` (no provider SDK) — same wire format as curl, easier to embed in Python apps. Snippets: `python/raw_{provider}.py`.

Gemini has no env tab; set `http_options.base_url` / `httpOptions.baseUrl` in code.

## Official SDK packages

| Language | OpenAI | Anthropic | Gemini |
|----------|--------|-----------|--------|
| Go | `github.com/openai/openai-go` | `github.com/anthropics/anthropic-sdk-go` | `google.golang.org/genai` |
| Node | `openai` | `@anthropic-ai/sdk` | `@google/genai` |
| Python | `openai` | `anthropic` | `google-genai` |
| curl | raw HTTP | | |
