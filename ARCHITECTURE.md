# LLM Proxy architecture

A tour of how the proxy forwards LLM requests to upstream providers while
layering auth, rate limits, cost tracking, circuit breaking, and PII redaction.
For usage and configuration knobs, see the [README](README.md).

## The one-paragraph model

Clients point their SDKs at `http://<proxy>:9002/<provider>/...` instead of the
vendor URL. Each provider is a thin `httputil.ReverseProxy` that strips the
`/<provider>` prefix and forwards credentials pass-through (API keys in headers,
or SigV4-signed bytes for Bedrock). A Gorilla mux middleware stack runs before
the proxy handler: validate `iw-*` proxy keys, enforce spend and rate limits,
scrub PII, parse token usage from responses, and record observability. Optional
backends — DynamoDB (keys), Redis (rate limits, circuit breaker, admin rollups),
S3 (row history), Presidio (PII) — are all driven by layered YAML under
`configs/`.

## Data flow

```
                    ┌─────────────────────────────────────────────┐
  Client SDK/curl   │  cmd/llm-proxy (mux router, default :9002)  │
                    └──────────────────────┬──────────────────────┘
                                           │
     ┌─────────────────────────────────────┼─────────────────────────────────────┐
     ▼                                     ▼                                     ▼
 MetaURLRewriting              APIKeyValidation / CostLimit              PIIRedact
 /meta/{user}/openai/…  →      iw-* → DynamoDB → real key               Presidio sidecar
 /openai/…                     daily/monthly spend caps                  (optional wire mode)
     │                                     │                                     │
     └─────────────────────────────────────┼─────────────────────────────────────┘
                                           ▼
              Logging → RateLimit → CORS → TokenParsing → PIIResponseRestore → Streaming
                                           │
                                           ▼
                              Provider reverse proxy (openai | anthropic | gemini | bedrock)
                                           │
                              ┌────────────┴────────────┐
                              ▼                         ▼
                     circuit.Transport          fake.Transport (fuzz only)
                     retries, breaker,          synthetic JSON + chaos
                     degraded 503 signal
                              │
                              ▼
                     Upstream LLM API
```

After the response returns, `TokenParsingMiddleware` calls each provider's
`ParseResponseMetadata` to build an `LLMResponseMetadata` snapshot. Registered
callbacks feed the cost tracker, usage stats, and (indirectly) admin rollups.

## Repository layout

| Path | Role |
| --- | --- |
| `cmd/llm-proxy/` | Main HTTP server — wiring, startup, graceful shutdown |
| `cmd/llm-proxy-keys/` | CLI for API key management |
| `cmd/llm-proxy-users/` | CLI for admin user roster |
| `cmd/config-validator/` | Validates merged YAML configs (`--validate-config`) |
| `internal/` | All production logic |
| `configs/` | Layered YAML (`base.yml` + env overlay + optional profile) |
| `web/` | React admin dashboard (Vite); embedded at build time with `-tags embed_ui` |
| `integration/` | Live provider tests, fuzz runners, language snippets |
| `examples/` | Bedrock SigV4 passthrough recipe, etc. |

## Configuration

Config merges in order:

1. `configs/base.yml`
2. `configs/{ENVIRONMENT}.yml` (default `dev`; set via `ENVIRONMENT`)
3. Optional `configs/{LLM_PROXY_CONFIG_PROFILE}.yml` (e.g. `sidecar`)

The root struct is `config.YAMLConfig`:

- `features.*` — toggles for cost tracking, API keys, rate limiting, circuit
  breaker, PII redaction, fake upstream, admin dashboard, row history
- `providers.*` — per-vendor enable flags, model lists, pricing tiers, aliases

Outside explicit local dev (`ENVIRONMENT=dev` or `LLM_PROXY_ALLOW_DEFAULT_CONFIG=1`),
a config load failure is fatal at startup.

## Packages

| Go package | Responsibility |
| --- | --- |
| `cmd/llm-proxy` | Composition root: load config, init globals, register providers, stack middleware, serve HTTP |
| `internal/config` | YAML load/merge/validate; `YAMLConfig`, `FeaturesConfig`, provider pricing schema |
| `internal/providers` | `Provider` interface, `ProviderManager`, per-vendor reverse proxies, token estimation |
| `internal/middleware` | Request/response pipeline: auth, limits, PII, logging, streaming, token parsing |
| `internal/apikeys` | DynamoDB-backed `iw-*` proxy keys; context helpers; per-key overrides |
| `internal/provision` | Mint upstream keys (OpenAI/Gemini/Anthropic) when creating proxy keys via admin |
| `internal/ratelimit` | Request/token limits; memory or Redis backend; scoped keys (global/provider/model/key/user) |
| `internal/circuit` | Failure classifier, per-model breaker store, `http.RoundTripper` wrapper, degraded signal |
| `internal/cost` | Pricing lookup, `CostTracker`, async workers, transports (file, DynamoDB, Datadog) |
| `internal/redact` | Presidio client; MASK/SEAL/REDACT placeholder policy |
| `internal/pii` | In-process PII redaction stats (metadata only — never raw PII) |
| `internal/redactapi` | Standalone `POST /redact` handler |
| `internal/admin` | `/admin` SPA + JSON API; Google OAuth; RBAC |
| `internal/adminusers` | DynamoDB admin user roster and roles |
| `internal/adminrollup` | Redis-backed daily metric rollups for admin charts |
| `internal/coststats` | In-process cost spend recorder for admin |
| `internal/usagestats` | Token volume recorder for admin Usage page |
| `internal/ratelimitstats` | Rate-limit event recorder |
| `internal/circuitstats` | Circuit activity recorder (memory or Redis) |
| `internal/history` | Buffered gzipped JSONL row history to local disk or S3 |
| `internal/fake` | Synthetic upstream responses for local fuzzing (gated by env var) |
| `web` | Embedded React admin UI (`embed_ui` build tag) |

## Provider layer

Every vendor implements `providers.Provider`:

| Method | Purpose |
| --- | --- |
| `Proxy()` | `httputil.ReverseProxy` handler |
| `IsStreamingRequest` | Detect SSE / streaming by path, body, or headers |
| `ParseResponseMetadata` | Extract tokens, model, finish reason from response body |
| `ValidateAPIKey` | Swap `iw-*` keys via `APIKeyStore`; pass through raw keys |
| `ExtractRequestModelAndMessages` | Model + text for rate-limit token estimation |
| `RegisterExtraRoutes` | Compatibility paths (Gemini `/v1/models/gemini…`, Bedrock `/model/…`) |
| `WrapTransport` | Injection point for circuit breaker and fake upstream layers |

| Provider | Upstream | Auth |
| --- | --- | --- |
| `openai` | `api.openai.com` | `Authorization: Bearer` pass-through |
| `anthropic` | `api.anthropic.com` | `x-api-key` pass-through |
| `gemini` | Google Generative Language API | `?key=` or header |
| `bedrock` | `bedrock-runtime.{region}.amazonaws.com` | SigV4 passthrough — proxy strips `/bedrock` only |

`CreateGenericDirector` strips `/<provider>` from the path before forwarding.
Bedrock is opt-in via `providers.bedrock.enabled: true`.

### `LLMResponseMetadata`

Canonical post-response snapshot consumed by cost tracking and stats:

```go
type LLMResponseMetadata struct {
    Model, Provider, RequestID, FinishReason string
    InputTokens, OutputTokens, TotalTokens, ThoughtTokens int
    CacheReadInputTokens, CacheCreationInputTokens int
    IsStreaming bool
    TTFBMS int64 // set by TokenParsingMiddleware
}
```

Providers leave unsupported fields at zero — the cost tracker treats zero as
"not reported", not "missed during parse".

## Middleware order

Order is fixed in `runServer` (`cmd/llm-proxy/main.go`). Earlier middleware
runs first on the way in; response wrappers run on the way out in reverse.

1. **MetaURLRewriting** — `/meta/{userID}/{provider}/…` → `/{provider}/…`
2. **APIKeyValidation** — resolve `iw-*` keys; stash `apikeys.APIKey` on context
3. **CostLimit** — per-key daily/monthly spend caps with optional reservation
4. **PIIRedact** — Presidio scrub (global and/or per-key override)
5. **TestMode** — integration-test circuit hooks (triple-gated; not for prod)
6. **Logging** — structured request logging
7. **RateLimit** — RPM/TPM/RPD/TPD with token estimate + post-response reconcile
8. **CORS** — browser compatibility
9. **TokenParsing** — capture response, parse usage, fire cost callbacks
10. **PIIResponseRestore** — restore MASK-tier placeholders in responses
11. **Streaming** — flush SSE chunks; stop on client disconnect

## API keys

Proxy keys are minted as `sk-<prefix>-<hex>` (default prefix base `iw`). Stored
in DynamoDB as `apikeys.APIKey`:

- `PK` — the proxy key string
- `ActualKey` — real upstream credential
- `DailyCostLimit` / `MonthlyCostLimit` — spend caps (cents)
- `RedactPII`, `AllowStreaming` — per-key PII overrides
- `RateLimitRPM/TPM/RPD/TPD` — per-key rate-limit overrides
- `Provisioned`, `UpstreamKeyID`, `UpstreamKind` — provisioner metadata

`apikeys.FromContext` lets downstream middleware read the resolved record without
re-querying DynamoDB.

## Rate limiting

`ratelimit.RateLimiter` exposes `Reserve` (pre-request) and `Reconcile`
(post-response, using actual input tokens from `X-LLM-Input-Tokens` or parsed
usage). Limits apply across scoped keys: global, provider, model, API key, user ID.

Backends: `memory` (single instance) or `redis` (cluster-wide). Token estimation
uses `Content-Length` or, for small JSON bodies, character-count of extracted
messages via `providers.EstimateRequestTokens`.

## Circuit breaker

Three cooperating pieces:

1. **`classifier.go`** — maps HTTP status/body to `FailureClass` (local rate
   limit, global rate limit, provider degraded, insufficient quota)
2. **`store.go`** — `Store` interface; `MemoryStore` or `RedisStore`; state keyed
   by `<provider>:<model>` with fallback to `<provider>`
3. **`transport.go`** — `http.RoundTripper` wrapper: check state → retry transient
   failures → record terminal failures → synthesize 503 with body marker
   `[LLM_PROXY_PROVIDER_DEGRADED]`

States: `closed` (healthy), `open` (fast-fail), `half_open` (single probe).

Optional **per-provider rollup** opens wholesale enforcement when N distinct model
breakers trip within a sliding window. Callers without a fallback can bypass
fast-fail per request via `X-LLM-Proxy-Bypass-Circuit` (when `bypass_allowed: true`).

Redis store failures fail open so a dead Redis never takes the proxy down.

## Cost tracking

`cost.CostTracker` loads per-model pricing from YAML, computes USD from
`LLMResponseMetadata`, and writes `CostRecord` rows through one or more
`Transport` implementations (file JSONL, DynamoDB, Datadog dogstatsd). Async
worker pool is optional (`features.cost_tracking.async`).

Fuzzy model-name matching handles responses where the model string does not
exactly match a configured pricing key.

## PII redaction

`redact.Redactor` calls a Presidio analyzer sidecar (`features.pii_redact.analyzer_url`).
Three placeholder tiers: MASK (restored in responses), SEAL (opaque), REDACT
(one-way markers). Wire mode (`wire_placeholders`) sends scrubbed text upstream;
otherwise only observability copies are redacted.

`POST /redact` (`internal/redactapi`) exposes standalone text redaction for
hooks and tools. `internal/pii.Recorder` stores metadata-only stats for admin.

## Admin dashboard

When `features.admin_dashboard.enabled`:

- **Auth** — Google OAuth (`/admin/auth/*`); dev bypass login in local configs
- **API** — `/admin/api/*` for keys, usage, cost, PII, rate limits, circuit activity
- **SPA** — React app in `web/`; production build embedded via `-tags embed_ui`
- **Rollups** — `adminrollup.Store` persists daily aggregates to Redis
- **RBAC** — `adminusers.Store` in DynamoDB (viewer / editor / admin roles)

Local dev: Vite on `:5173` proxies API calls to the Go server on `:9002`, or use
the embedded SPA at `http://localhost:9002/admin/`.

## Row history

`internal/history.Sink` buffers raw events (cost, PII, usage, ratelimit) and
flushes gzipped JSONL to local disk or S3 on size, count, time, or shutdown.
Partitioned object keys support Athena JSON SerDe queries. Disabled by default
(`features.history.backend: none`).

## HTTP routes

| Route | Handler |
| --- | --- |
| `GET /health` | Provider status + circuit breaker state |
| `POST /redact` | Standalone PII API (optional) |
| `/{provider}/*` | Reverse proxy to upstream |
| `/meta/{userID}/{provider}/*` | Rewritten to `/{provider}/*` |
| `/admin/*` | Dashboard SPA + API |
| Provider extras | Gemini `/v1/models/gemini…`; Bedrock `/model/…` |

## Binaries and build

```bash
make build                    # proxy without embedded UI
go build -tags embed_ui ./cmd/llm-proxy   # production (embeds web/dist)
```

Production Docker builds use `-tags embed_ui` (see `build/Dockerfile.prod`).
Without the tag, `web/embed_stub.go` returns an empty FS and `/admin` static
assets are unavailable.

## Testing

| Layer | Location |
| --- | --- |
| Unit tests | `internal/*_test.go` (~78% coverage on `./internal/...`) |
| Provider integration | `internal/providers/*_test.go` (real APIs when keys set) |
| Live integration | `integration/live/` |
| Fuzz / chaos | `integration/fuzz/`, `configs/fuzz.yml`, `LLM_PROXY_ALLOW_FAKE_MODE=1` |

## Key decisions

- **Credential pass-through.** The proxy validates `iw-*` keys and swaps them, but
  never holds vendor master keys in config — callers (or DynamoDB records) supply
  real credentials. Bedrock follows the same contract: SigV4 bytes are forwarded
  verbatim; only the `/bedrock` URL prefix is stripped.
- **Provider-per-transport wrapping.** Circuit breaker and fake upstream inject at
  the `http.RoundTripper` layer per provider, not globally, so each vendor's retry
  and classification logic stays isolated.
- **Body marker for degradation.** Synthetic 503 responses embed
  `[LLM_PROXY_PROVIDER_DEGRADED]` in the JSON body because SDK exception wrappers
  often hide custom headers; `str(exception)` is the lowest common denominator.
- **Redis fail-open.** Circuit breaker and rate limiter fall back to per-instance
  best effort when Redis is unavailable at startup or runtime — proxy availability
  takes priority over distributed coordination.
- **Observability triple store.** Each domain (cost, PII, usage, rate limit) can
  use in-process memory (live admin), Redis rollups (daily charts), and row history
  (raw events for debugging) independently.

## Adding a provider

1. Create `internal/providers/<name>.go` implementing `Provider`
2. Use `CreateGenericDirector` + `newProxyTransport` (or a custom transport)
3. Implement streaming detection and `ParseResponseMetadata` for that vendor's JSON/SSE
4. Add `WrapTransport` for circuit/fake injection (copy an existing provider)
5. Register in `registerProviders` in `cmd/llm-proxy/main.go`
6. Add pricing entries under `providers.<name>.models` in YAML
7. Add integration tests

For request-signed upstreams (SigV4, mTLS), use passthrough mode: no-op
`ValidateAPIKey`, never mutate signed headers or body, strip only your URL prefix.
`internal/providers/bedrock.go` is the reference implementation.
