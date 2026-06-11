# llm-proxy live integration checks

Standalone Go module that exercises a **running** llm-proxy instance using the official provider SDKs (`openai-go`, `anthropic-sdk-go`, `google.golang.org/genai`) pointed at the proxy base URL. It does not import anything from the main `llm-proxy` module.

## Dependencies

```bash
cd integration
go mod tidy
```

| SDK | Module |
|-----|--------|
| OpenAI | `github.com/openai/openai-go` |
| Anthropic | `github.com/anthropics/anthropic-sdk-go` |
| Gemini | `google.golang.org/genai` |

Each client is configured with `BaseURL` → `{proxy}/openai/v1`, `{proxy}/anthropic`, or `{proxy}/gemini`. A wrapping `http.Transport` captures llm-proxy response headers (`X-LLM-*`, `X-RateLimit-*`) after SDK calls.

## Share-box snippet tests

Runnable copies of every **Drop-in usage** tab on the share page live under [`snippets/`](snippets/README.md) (curl, Go, Node, Python). Dependencies install automatically:

```bash
make install-snippet-deps   # node_modules + python/.venv + go mod download
make test-live-snippets
```

## Prerequisites

1. Proxy running locally (default `http://localhost:9002`):

   ```bash
   docker compose up -d
   ```

2. Provider API keys in the environment (only suites that need them run):

   ```bash
   export OPENAI_API_KEY=sk-...
   export ANTHROPIC_API_KEY=sk-ant-...
   export GEMINI_API_KEY=...
   ```

3. PII / Presidio (enabled in `configs/dev.yml` by default):

   ```bash
   make test-pii-up
   # or: docker compose --profile pii_redact up -d presidio
   ```

   Restart the proxy after changing YAML so the redaction middleware loads.
   Compose sets `PRESIDIO_ANALYZER_URL=http://presidio:3000` for the proxy container.
   If Presidio was started from a different checkout, it may land on another
   Docker network and the proxy will fail-open without detecting PII — `make
   test-pii-up` removes the stale container and recreates it on the right network.

   **Package-level PII integration tests** (Presidio required, no running proxy):

   ```bash
   make test-pii
   ```

   Runs `TestIntegration_*` in `./internal/redact/...` (Redact + Scrub tiers) and
   `./internal/middleware/...` (legacy observability mode + wire scrub/restore stack).

4. Cost tracking — file transport writes to `./logs/cost-tracking.jsonl` (mounted in compose). Run from repo root or pass `-cost-file`.

## Run

From this directory:

```bash
go run ./cmd/llm-proxy-live
```

Or build a binary:

```bash
go build -o bin/llm-proxy-live ./cmd/llm-proxy-live
./bin/llm-proxy-live
```

### Flags / env

| Flag | Env | Default |
|------|-----|---------|
| `-base-url` | `PROXY_BASE_URL` | `http://localhost:9002` |
| `-presidio-url` | `PRESIDIO_ANALYZER_URL` | `http://localhost:5004` |
| `-cost-file` | `COST_TRACKING_FILE` | `../logs/cost-tracking.jsonl` |
| `-snippets-dir` | `LLM_PROXY_SNIPPETS_DIR` | `snippets` |
| `-suite` | — | `all` |
| `-timeout` | — | `90` |
| `-verbose` | — | `true` (progress on stderr; PASS/FAIL streamed to stdout) |

### Suite selection

```bash
go run ./cmd/llm-proxy-live -suite health,admin,ratelimit
go run ./cmd/llm-proxy-live -suite openai,anthropic,gemini
go run ./cmd/llm-proxy-live -suite presidio,pii
go run ./cmd/llm-proxy-live -suite snippets
```

## What each suite checks

| Suite | Checks |
|-------|--------|
| `health` | `GET /health` |
| `admin` | dev-login, `/admin/api/me`, config, rate-limits, cost config (+ live stats when enabled), **PII endpoint**, create+delete key |
| `openai` | passthrough + `iw:` proxy key, token headers |
| `anthropic` | passthrough + `iw:` key |
| `gemini` | passthrough + `iw:` key |
| `ratelimit` | per-key `rate_limit_rpm: 1` → second request returns 429 with `X-RateLimit-*` |
| `cost` | JSONL record after proxy call + `/admin/api/cost` `stats.spend_today_usd` > 0 |
| `presidio` | sidecar `/health` |
| `pii` | admin stats live; PII-bearing request via `redact_pii` key; `requests_scanned` / `requests_with_pii` / `entities_total` / recent events increase; **wire-restore** (MASK email round-trip through running proxy); **wire-seal** (SSN stays opaque to client) |
| `snippets` | Every share-box tab: curl, Raw HTTP (httpx), Go, Node, Python (+ env tabs for OpenAI/Anthropic) |

Skipped suites (missing keys, disabled features, unreachable sidecar) do not fail the run.

## Exit code

- `0` — all executed checks passed (skips are ok)
- `1` — one or more failures
- `2` — setup error
