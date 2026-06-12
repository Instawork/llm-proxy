# LLM Proxy

[![CircleCI](https://circleci.com/gh/Instawork/llm-proxy.svg?style=svg)](https://circleci.com/gh/Instawork/llm-proxy)

<img height="250" alt="Screenshot 2025-09-08 at 10 10 08 AM" src="https://github.com/user-attachments/assets/5c6ecf7f-14bf-4d67-ba48-f250c80e3205" />

A simple, Go-based alternative to the `litellm` proxy, without all the extra stuff you don't need! A modular reverse proxy that forwards requests to various LLM providers (OpenAI, Anthropic, Gemini, AWS Bedrock) using Go and the Gorilla web toolkit.

## Features

- **Multi-provider support**: Full support for OpenAI, Anthropic, Gemini, and AWS Bedrock
- **Streaming Support**: Native streaming support for all providers
- **OpenAI Integration**: Complete OpenAI API compatibility with `/openai` prefix
- **Anthropic Integration**: Claude API support with `/anthropic` prefix
- **Gemini Integration**: Google Gemini API support with `/gemini` prefix
- **AWS Bedrock Integration**: Anthropic Claude (and any other Converse API model) on Bedrock via a transparent SigV4-passthrough — clients sign with their own AWS credentials, proxy forwards bytes verbatim
- **Comprehensive Logging**: Request/response monitoring with streaming detection
- **CORS Support**: Browser-based application compatibility
- **Health Check**: Detailed health status for all providers
- **Configurable Port**: Environment variable configuration (default: 9002)
- **Rate Limiting (experimental)**: Optional request/token-based limits per user/API key/model/provider
- **Circuit Breaker**: Opt-in provider health tracking that classifies upstream failures, retries transient / rate-limit errors, and emits a dedicated degraded-signal response so clients can fall back to another provider during an outage

## Quick Start

```bash
# Get help on available commands
make help

# Install dependencies and build
make install build

# Run the proxy
make run

# Or run in development mode
make dev
```

### Making requests

Once the proxy is running, you can make requests to LLM providers through the proxy:

```bash
# Health check (shows all provider statuses)
curl http://localhost:9002/health

# OpenAI Chat completions (replace YOUR_API_KEY with your actual OpenAI API key)
curl -X POST http://localhost:9002/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello, world!"}],
    "max_tokens": 50
  }'

# OpenAI Streaming
curl -X POST http://localhost:9002/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true,
    "stream_options": {"include_usage": true}
  }'

# Anthropic Messages
curl -X POST http://localhost:9002/anthropic/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: YOUR_API_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "claude-3-sonnet-20240229",
    "max_tokens": 100,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'

# Gemini Generate Content
curl -X POST http://localhost:9002/gemini/v1/models/gemini-pro:generateContent?key=YOUR_API_KEY \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"parts": [{"text": "Hello!"}]}]
  }'
```

#### AWS Bedrock (transparent SigV4 passthrough)

> **Why Bedrock is different.** AWS Bedrock authenticates with request-signed
> SigV4 — not a static API key in a header. The proxy does **not** sign for
> you because (a) it preserves the OSS proxy's "credentials pass-through"
> contract, (b) it lets you use whatever AWS identity you already have (IAM
> role, IAM user, AssumeRole, IRSA, env vars), and (c) it works for users who
> don't run the proxy on AWS. The proxy is a transparent reverse proxy: it
> strips the `/bedrock` URL prefix and forwards bytes verbatim to
> `bedrock-runtime.{region}.amazonaws.com`. The signed `Host` and
> `Authorization` headers ride through unchanged. See the
> [architecture section](#architecture) for the full contract.

Bedrock is opt-in via `providers.bedrock.enabled: true` in your config
(see `configs/base.yml` and the staging/production overrides). Once enabled,
the easiest way to talk to it is via the boto3 ``before-send`` hook recipe —
the runnable example lives at
[`examples/bedrock-passthrough/python.py`](examples/bedrock-passthrough/python.py),
and the abbreviated form is:

```python
import boto3
from langchain_aws import ChatBedrockConverse

PROXY = "http://localhost:9002"
REGION = "us-west-2"

session = boto3.Session(region_name=REGION)

def _route_to_proxy(request, **_):
    aws = f"https://bedrock-runtime.{REGION}.amazonaws.com"
    if request.url.startswith(aws):
        request.url = request.url.replace(aws, f"{PROXY}/bedrock", 1)

# before-send fires AFTER signing — Authorization is computed against the real
# Bedrock host, then we just rewrite the destination URL.
session.events.register("before-send.bedrock-runtime", _route_to_proxy)

llm = ChatBedrockConverse(
    model_id="us.anthropic.claude-sonnet-4-5-20250929-v1:0",
    client=session.client("bedrock-runtime"),
)
print(llm.invoke("hello").content)
```

> **Why no plain curl example?** Hand-rolling a SigV4 signature with curl is
> nontrivial — every header in the canonical request has to be signed in
> alphabetic order against the body's SHA-256 digest. If you need to drive
> Bedrock from a shell rather than Python, reach for
> [`awscurl`](https://github.com/okigan/awscurl) (which wraps the same
> credential chain boto3 uses) or copy the recipe above into a one-off
> script.

## Testing

### Unit test coverage

_Updated: 2026-06-12 — refresh with `make test-cover` (`go test -race ./internal/... -short -skip Integration`)._

| Package | Coverage |
|---------|----------|
| **Total (`./internal/...`)** | **78.5%** |
| `internal/admin` | 37.1% |
| `internal/adminrollup` | 73.4% |
| `internal/apikeys` | 85.1% |
| `internal/circuit` | 85.3% |
| `internal/config` | 88.5% |
| `internal/cost` | 81.7% |
| `internal/coststats` | 95.5% |
| `internal/history` | 78.6% |
| `internal/middleware` | 89.1% |
| `internal/pii` | 81.6% |
| `internal/providers` | 85.9% |
| `internal/ratelimit` | 85.1% |
| `internal/ratelimitstats` | 80.4% |
| `internal/redact` | 89.3% |
| `internal/usagestats` | 100.0% |

The project includes comprehensive integration tests for all providers:

```bash
# Run all tests
make test-all

# Run unit tests with coverage report (coverage.out)
make test-cover

# Run tests for specific providers
make test-openai
make test-anthropic
make test-gemini

# Run health check tests only
make test-health

# Check environment variables
make env-check
```

### Setting up API Keys

To run integration tests, you need to set up environment variables:

```bash
export OPENAI_API_KEY=your_openai_key
export ANTHROPIC_API_KEY=your_anthropic_key
export GEMINI_API_KEY=your_gemini_key
```

Bedrock integration tests do **not** use an API key — they use whatever AWS
credentials are visible to the boto3 default chain (`AWS_PROFILE`, IRSA, IMDS,
or `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` env vars). The proxy itself
never sees or validates these — they are encoded into the SigV4 signature
that the client computes and the proxy forwards verbatim.

## Configuration

- `ENVIRONMENT`: Selects the deploy overlay YAML (`configs/{ENVIRONMENT}.yml`, default `dev`).
- `LLM_PROXY_CONFIG_PROFILE`: Optional second overlay merged after the env file (e.g. `sidecar` for co-located containers in the same task — keeps `ENVIRONMENT=production` while disabling PII redaction and the admin dashboard via `configs/sidecar.yml`).
- `PORT`: Environment variable to set the server port (default: 9002)
- `AWS_REGION`: Region for the upstream Bedrock endpoint host (default:
  `us-west-2`). Only consulted when `providers.bedrock.enabled` is true.
  Bedrock pricing keys live under `providers.bedrock.models.*` in the YAML
  config exactly like other providers — see
  [`configs/base.yml`](configs/base.yml) for the schema.

### Rate Limiting (Experimental)

- Disabled by default. Enable via config: see `configs/base.yml` and `configs/dev.yml`.
- Supports provisional token estimation with post-response reconciliation using `X-LLM-Input-Tokens` (input tokens only).
- Returns `429 Too Many Requests` with `Retry-After` and `X-RateLimit-*` headers when throttled.
- Set `backend: "redis"` and configure `redis.url` (or `redis.address` for dev) for multi-instance rate limiting. Dev docker-compose uses logical DB 4 on the bundled Redis service; production uses `${REDIS_URL}` from SSM (`/llm-proxy/redis_url`).

Minimal dev example (see `configs/dev.yml` for a full setup):

```yaml
features:
  rate_limiting:
    enabled: true
    backend: "memory" # single instance only
    estimation:
      max_sample_bytes: 20000
      bytes_per_token: 4 # Fallback to request size (Content-Length based)
      chars_per_token: 4 # Default for message-based estimation
      # Optional per-provider overrides (recommended)
      provider_chars_per_token:
        openai: 5      # ~185–190 tokens per 1k chars (from scripts/token_estimation.py)
        anthropic: 3   # ~290–315 tokens per 1k chars (from scripts/token_estimation.py)
    limits:
      requests_per_minute: 0   # 0 = unlimited (dev defaults)
      tokens_per_minute: 0
```

#### Token estimation behavior

- We currently account for and reconcile only input tokens. Output tokens are not yet considered for rate limits/credits.
- For small JSON requests (size controlled by `max_sample_bytes`), the proxy extracts textual message content via provider-specific parsers and estimates tokens by character count using `chars_per_token` (with per-provider overrides).
- Default per-provider values come from benchmarks produced by `scripts/token_estimation.py`. You can run the script to generate your own table and override values in config.
- Non-text modalities (images/videos) are not supported for estimation at this time and will fall back to credit-based only behavior essentially via `max_sample_bytes`.
- Optimistic first request: to avoid estimation blocking initial traffic, the first token-bearing request in a window (when current token count is zero) is allowed even if token limits would otherwise apply. Subsequent requests are enforced normally.

### Circuit Breaker & Provider Degradation

Disabled by default. Enable it when you want the proxy to detect provider outages (as opposed to individual request failures) and broadcast that state to clients so they can switch to a fallback provider instead of retry-storming a dead upstream.

#### What it does

- **Classifies every upstream failure** as one of: local rate-limit, global rate-limit, or provider-degraded. Provider-specific rules — e.g. Anthropic `529 Overloaded` → degraded, Gemini `429 RESOURCE_EXHAUSTED` → local rate-limit, OpenAI 429 with both `x-ratelimit-remaining-requests` and `x-ratelimit-remaining-tokens` at zero → global rate-limit.
- **Retries transient failures** with jittered exponential backoff (configurable attempts). Rate-limit retries honour `Retry-After`. Sustained global rate limits escalate to degraded after a configurable window.
- **Tracks state per `<provider>:<model>` key** so a single misbehaving model (e.g. one preview tier) cannot blast-radius onto its sibling models on the same provider. Falls back to bare `<provider>` keying when the model cannot be extracted (oversize body, missing model field) so coverage degrades gracefully rather than disappearing.
- **Opens the per-key circuit** once that key accumulates enough terminal failures inside a sliding window. While open, every request to that key is fast-failed locally without touching the network. After a cooldown the proxy issues one half-open probe; success closes the circuit, failure re-opens it for another cooldown.
- **Optional per-provider rollup** detects wholesale outages: when N distinct per-key breakers for the same provider open inside a configurable rollup window, ALL keys for that provider — including ones whose individual breaker is still Closed — fast-fail. Recovers automatically as keys come back via half-open probes (each successful probe drops the key out of the rollup window).
- **Bypass safety valve**: callers without a fallback can opt out of fast-fail for a single request by setting the `X-LLM-Proxy-Bypass-Circuit` header (or `?llm_proxy_bypass_circuit=1` query param). Bypass requests still feed observability — observed 5xxs are still credited to the breaker — but the proxy never returns a synthetic 503 and never closes the circuit on success. See "Bypassing the breaker" below.
- **Surfaces state** on `GET /health` per provider: `circuit_state` (`closed` / `open` / `half_open`), `circuit_failures`, `circuit_cooldown_until`, plus a `rollup` block listing the currently-degraded `open_keys` and rollup count when the rollup feature is enabled.

#### The degraded signal

When a provider is degraded (terminal retry exhaustion, open-circuit fast-fail, or half-open probe failure), the proxy returns a synthetic response:

- **HTTP 503** status
- **`X-Llm-Proxy-Error-Class: provider_degraded`** response header
- **JSON body** containing a configurable marker substring — `[LLM_PROXY_PROVIDER_DEGRADED]` by default:

  ```json
  {"error":{"message":"[LLM_PROXY_PROVIDER_DEGRADED] Provider openai is currently degraded or unavailable. Please try again later.","type":"provider_degraded","code":"provider_degraded"}}
  ```

Clients detect the degraded condition by looking for that substring anywhere in `str(exception)` (or the response body) and can then fall back to a different provider / model.

#### Why a body marker and not just a status code?

The 503 status and `X-Llm-Proxy-Error-Class` header are always set, but on their own they are not enough:

1. **5xx is ambiguous.** The proxy streams real provider 5xx responses straight through (Anthropic 529, OpenAI 500/502/503/504, Gemini 500/503, …). A client that only looks at status code cannot tell a passthrough upstream 503 from a proxy-synthesised "circuit open" 503 — they have very different retry / fallback semantics.
2. **4xx is wrong.** The caller did nothing wrong; the upstream is degraded. 4xx would break SDK retry logic that (correctly) refuses to retry 4xx.
3. **A novel status code (e.g. 599) is hostile.** Many reverse proxies, CDNs, and HTTP clients coerce unknown codes to 500 or strip them entirely. The OpenAI / Anthropic / Google SDKs all map any ≥500 response into a generic `APIError` / `ServerError` class, so a custom code buys you nothing downstream.
4. **Custom headers get stripped by SDK exception wrappers.** By the time an HTTPS error propagates up through e.g. the OpenAI Python SDK or LangChain, the caller typically only sees `str(exception)` (the body). Response headers are usually only accessible if the caller catches a specific, provider-native exception type _before_ any framework wraps it. A body substring survives every wrapping layer.

So the contract is **503 + header + body marker, and clients can key off any of them**. The body marker is the most reliable because exception message text is the lowest common denominator across every SDK stack.

#### Configuration

Config lives under `features.circuit_breaker` in YAML. All values shown below are defaults:

```yaml
features:
  circuit_breaker:
    enabled: true                       # gate the whole feature
    backend: "memory"                   # "memory" (single instance) or "redis" (multi-instance)
    failure_threshold: 5                # terminal failures in the window that trip the circuit
    window_seconds: 120                 # sliding-window TTL for the failure counter
    cooldown_seconds: 300               # how long the circuit stays open before a probe
    max_transient_retries: 2            # retries for degraded-class failures
    max_rate_limit_retries: 2           # retries for rate-limit failures
    retry_contribution_mode: "log"      # "off" | "log" | "on" — whether retried failures count
    global_rate_limit_escalation_window: 60  # seconds of sustained global 429s → degraded
    degraded_signal: ""                 # override the body marker; empty → "[LLM_PROXY_PROVIDER_DEGRADED]"
    test_mode_enabled: false            # prod: leave off.  Enables X-LLM-Proxy-Test-Mode header.
    bypass_allowed: true                # honour X-LLM-Proxy-Bypass-Circuit; set false to disable.
    bypass_reason_allowlist: []         # if non-empty, only these values appear verbatim
                                        # on the `circuit.bypass` Datadog tag — anything
                                        # else is normalised to `other`.  Bounds tag
                                        # cardinality.  Empty (default) accepts any
                                        # well-formed reason.
    per_provider_rollup_threshold: 0    # 0 = rollup disabled.  Recommended prod: 3.
    per_provider_rollup_window_seconds: 300  # sliding window for the rollup signal.
    # redis:                            # required when backend == "redis"
    #   address: "localhost:6379"
    #   password: ""
    #   db: 0
```

The `degraded_signal` field lets you embed a project- or company-specific tag in the response body (e.g. `"[MY_COMPANY_UPSTREAM_DOWN]"`). Clients then pattern-match on that tag instead of the default. Leaving it empty keeps the default, which is the right choice for most deployments.

#### Testing the circuit breaker without touching real providers

When `test_mode_enabled: true`, the proxy honours an `X-LLM-Proxy-Test-Mode` request header (or `llm_proxy_test_mode` query parameter, for SDKs like the Google Gemini client that don't let you pass custom headers):

- `force_degraded` — return the synthesised 503 / degraded body immediately, as if the circuit were open.
- `force_transient_recover` — fail the first attempt with a degraded error and succeed on the retry, so you can exercise the retry loop without the circuit tripping.

This is intended strictly for integration tests — leave `test_mode_enabled` off in production.

#### Bypassing the breaker

Some callers cannot tolerate a fast-fail with a synthetic 503 — they have no fallback wired up and would rather try the real upstream and accept whatever it returns (including a real 5xx). For those callers the proxy honours an opt-in bypass header:

```http
X-LLM-Proxy-Bypass-Circuit: 1                       # truthy values: 1, true, yes
X-LLM-Proxy-Bypass-Reason: no_fallback_configured   # optional; tagged into the bypass metric (must be in bypass_reason_allowlist or it tags as `other`)
```

…or, for SDKs that cannot set custom headers (notably the Google Gemini client), the same options as URL query parameters:

```text
?llm_proxy_bypass_circuit=1&llm_proxy_bypass_reason=no_fallback_configured
```

When a bypass request arrives, the proxy:

- **Skips** the per-key circuit state check, the per-provider rollup signal, and the half-open probe-slot guard.
- **Calls the upstream once** and returns whatever it answers — including real 5xxs. **No retries are performed for bypass requests** — bypass is deliberately single-shot so callers know exactly what they paid for. If you want to absorb transient blips, drive that retry loop yourself on top of the bypass call (e.g. tenacity / SDK-level retry).
- **Strips** the bypass markers (header AND query param) before forwarding so providers don't see proxy-internal diagnostics.
- **Still feeds observability**: any `provider_degraded` response observed during a bypass call is credited to the per-key breaker and counted toward the rollup window. So bypass traffic cannot blind the dashboards or hide a wholesale outage.
- **Never closes an Open breaker on success**: only a real half-open probe is "this provider has recovered" evidence. A successful bypass call does not flip state in either direction.
- Emits a `circuit.bypass` dogstatsd counter tagged with `provider`, `model`, `reason`, and `outcome` (`provider_degraded` / `localized_rate_limit` / `global_rate_limit` / `none`) so operators can audit how the safety valve is being used.

Set `bypass_allowed: false` in YAML to disable the safety valve entirely (e.g. once every caller has fallback logic wired up and you want to enforce that).

> **In `mode: log` (observe-only), bypass is a no-op for traffic shaping** — no fast-fail can occur in the first place, so there is nothing to bypass. The proxy still strips bypass markers (header AND query param) from the request before forwarding upstream so providers never see proxy-internal diagnostics, and it still accounts the call against the breaker / rollup like any other observed request. The `circuit.bypass` counter is not emitted in log mode (since the safety valve was not actually exercised); use the `circuit.fail_record` / rollup metrics to understand observed health.

##### Bypass reasons and Datadog cardinality

The `X-LLM-Proxy-Bypass-Reason` header / `llm_proxy_bypass_reason` query param is forwarded into the `circuit.bypass` metric's `reason` tag. To keep dogstatsd tag cardinality bounded:

- Reasons are **normalised** to lowercase `[a-z0-9_-]` (any other character collapses to `_`) and capped at 64 characters before tagging — so an attacker cannot inject tag separators or balloon the tag length.
- Empty / whitespace-only reasons tag as `unspecified`.
- If `bypass_reason_allowlist` is non-empty, any reason NOT in it tags as `other`. Recommended production vocabulary:

  | Reason value             | When to use                                                |
  | ------------------------ | ---------------------------------------------------------- |
  | `no_fallback_configured` | Caller has no provider/model fallback wired up.            |
  | `manual_debug`           | Operator override during one-off triage / debugging.       |
  | `final_retry_tier`       | Last-resort attempt after the caller's own retries failed. |
  | `force_real_upstream`    | Integration test exercising the real provider path.        |

  Anything else → `other`. Pick the canonical vocabulary that matches your infrastructure; the values above are illustrative.

#### Per-model keying and per-provider rollup

The circuit breaker tracks state per `<provider>:<model>` key (e.g. `gemini:gemini-2.5-pro-preview`), with a graceful fallback to bare `<provider>` when the model cannot be extracted from the request body. This isolates the blast radius of a single misbehaving model to that one model — sibling models on the same provider keep flowing.

For wholesale outages where many models on the same provider degrade simultaneously, the optional per-provider rollup escalates back to provider-wide enforcement:

- Every per-key breaker that transitions Closed → Open writes a `(timestamp, key)` entry into a per-provider rollup sliding window. Membership is **dedup-by-key**: a single flapping model does not multiply-count.
- When the count of distinct entries within the window meets `per_provider_rollup_threshold`, the rollup is considered Open and ALL traffic for that provider is fast-failed (synthetic 503 + DegradedSignal), regardless of which per-key breakers are individually Open.
- **Long-burn outages stay tripped**: every failed half-open probe re-arms the offending key's timestamp inside the window, so the same N keys continuously down for hours keep tripping the rollup instead of silently aging out after the first window expires.
- The rollup auto-recovers as per-key half-open probes succeed (each successful probe drops the recovered key out of the rollup window). The window itself only ages out a key once that key has fully gone silent for `per_provider_rollup_window_seconds`.

In short: the rollup count is "how many distinct models on this provider are currently degraded" — not "how many opened in the last N seconds".

The rollup is opt-in: leave `per_provider_rollup_threshold: 0` (the default) to keep behaviour identical to per-key keying alone. Recommended production starting point: `per_provider_rollup_threshold: 3`, `per_provider_rollup_window_seconds: 300` — i.e. trip wholesale enforcement only when at least 3 distinct model breakers on the same provider are concurrently degraded within a 5-minute window.

##### Operational notes

A few subtleties worth knowing if you're operating this in production:

- **Probe coordination across many models.** When N per-model breakers go HalfOpen simultaneously (e.g. immediately after a rollup cooldown), each gets its own probe slot — so the upstream sees a small N-sized burst at the moment of recovery. Rare in practice (it requires N concurrent Closed→Open transitions to begin with), but worth watching during incident postmortems.
- **Stale per-model keys.** If a model is deprecated and stops receiving traffic, its Redis state keys age out naturally via TTL (`max(2 × cooldown_seconds, window_seconds)` for the failure window, plus shorter TTLs on state / probe locks). No explicit cleanup is required.
- **Rate-limit retries vs. rollup.** Rate-limit retries (`max_rate_limit_retries`) are not classified as terminal failures unless they escalate via `global_rate_limit_escalation_window`, so they do not directly contribute to either the per-key breaker count or the rollup window. Only `provider_degraded`-class failures do.

## API Endpoints

### General

- `GET /health` - Health check endpoint for all providers. When the circuit breaker is enabled the response also includes a top-level `circuit_breaker.providers[<name>]` block per provider with `state`, `failures`, optional `cooldown_until`, and (when the rollup feature is enabled) a `rollup` sub-block reporting `open` / `count` / `threshold` / `window_seconds` and `open_keys` (the set of currently-degraded `<provider>:<model>` keys). The legacy `providers[<name>].circuit_state` / `circuit_failures` / `circuit_cooldown_until` fields remain populated for back-compat.

### OpenAI

- `POST /openai/v1/chat/completions` - OpenAI chat completions endpoint (streaming supported)
- `POST /openai/v1/completions` - OpenAI completions endpoint (streaming supported)
- `*  /openai/v1/*` - All other OpenAI API endpoints

### Anthropic

- `POST /anthropic/v1/messages` - Anthropic messages endpoint (streaming supported)
- `*  /anthropic/v1/*` - All other Anthropic API endpoints

### Gemini

- `POST /gemini/v1/models/{model}:generateContent` - Gemini content generation (streaming supported)
- `POST /gemini/v1/models/{model}:streamGenerateContent` - Explicit streaming endpoint
- `*  /gemini/v1/*` - All other Gemini API endpoints

### AWS Bedrock

- `POST /bedrock/model/{modelId}/converse` - Bedrock Converse non-streaming
- `POST /bedrock/model/{modelId}/converse-stream` - Bedrock Converse streaming (AWS event-stream framing)
- `*  /bedrock/*` - All other Bedrock Runtime endpoints

Bedrock routes mirror AWS's own path shape (`/model/{modelId}/...`) under a
`/bedrock` prefix that the proxy strips before forwarding, so client SDKs sign
against the canonical AWS URL and the upstream sees a path identical to what
was signed. See the [architecture section](#architecture) for the passthrough
contract.

## Architecture

The proxy is built with a modular architecture:

- **`main.go`**: Core server setup, middleware, and provider registration
- **`providers/openai.go`**: OpenAI-specific proxy implementation with streaming support
- **`providers/anthropic.go`**: Anthropic proxy implementation with streaming support
- **`providers/gemini.go`**: Gemini proxy implementation with streaming support
- **`providers/bedrock.go`**: AWS Bedrock transparent SigV4 passthrough with eventstream usage parsing
- **`providers/provider.go`**: Common interfaces and provider management

### Credential modes

Most providers receive a static API key in a header which the proxy validates
and forwards. Bedrock receives a SigV4-signed request which the proxy forwards
verbatim. Both are "credential pass-through" — the SigV4 case just happens to
involve more headers (`Authorization`, `X-Amz-Date`, `X-Amz-Content-Sha256`,
`X-Amz-Security-Token`) and a body content hash. The proxy never inspects or
mutates any of them, so any change to the body, the path, or the Host would
invalidate the signature; for that reason the Bedrock director strips only the
`/bedrock` URL prefix (from both `Path` and `RawPath` — preserving
URL-encoded characters like `%3A` that boto3 emits for the `:` in model
inference-profile IDs) and leaves every signed header alone.

Each provider implements its own:

- Route registration
- Request/response handling with streaming support
- Error handling
- Health status reporting
- Response metadata parsing

## Development

### Local development: Docker + the admin dashboard

Local dev runs the Go proxy, its datastores, and the admin dashboard from
Docker Compose:

1. **Start the full dev stack**:

   ```bash
   make docker-compose-up        # ENVIRONMENT=dev docker compose up -d
   ```

   This starts four containers: `llm-proxy` (the Go server, live-reloaded via
   `air` on `:9002`), `redis` (circuit breaker / rate limiting / admin
   rollups), `dynamodb` (local API-key + cost-tracking tables), and `web` (the
   Vite admin dashboard on `:5173`). Check them with `docker compose ps`; tail
   logs with `make docker-compose-logs`.

2. **Open the admin dashboard**:

   | URL | Served by | Use when |
   | --- | --- | --- |
   | `http://localhost:9002/admin/` | The Go proxy (embedded build) | You just want to click around; matches production exactly. No extra process. |
   | `http://localhost:5173/admin/` | The Vite dev server from Docker Compose | You're editing `web/` and want hot reload. |

   If you prefer to run Vite directly on the host, stop the `web` container and
   start the same dev server from `web/`:

   ```bash
   cd web
   npm install        # first time only
   npm run dev        # serves http://localhost:5173/admin/
   ```

   Vite proxies `/admin/api` and `/admin/auth` to the Go proxy at `:9002`
   (override with `VITE_API_PROXY_TARGET`), so the proxy containers must be up
   for the dashboard to load data or log in.

> **`localhost:5173` refused to connect?** Check that the `web` container is
> running with `docker compose ps`, or use the embedded SPA at
> `http://localhost:9002/admin/` instead.

### Available Make Commands

```bash
# Get help on all available commands
make help

# Code quality
make check         # Run all code quality checks
make fmt           # Format Go code
make vet           # Run go vet
make lint          # Run golint

# Building
make build         # Build the binary
make clean         # Clean build artifacts
make install       # Install dependencies

# Running
make run           # Run the built binary
make dev           # Run in development mode

# Testing
make test          # Run unit tests
make test-all      # Run all tests including integration
make test-openai   # Run OpenAI tests only
make test-anthropic # Run Anthropic tests only
make test-gemini   # Run Gemini tests only
```

### Test Structure

Tests are organized by provider:

- **`openai_test.go`**: OpenAI integration tests (streaming and non-streaming)
- **`anthropic_test.go`**: Anthropic integration tests (streaming and non-streaming)
- **`gemini_test.go`**: Gemini integration tests (streaming and non-streaming)
- **`common_test.go`**: Health check and environment variable tests
- **`test_helpers.go`**: Shared test utilities

### Historical event archive (row history)

Each observability domain (cost, PII, usage, rate limit) exposes three independent backends:

- **Memory** — in-process `Recorder` snapshots for the admin dashboard
- **Redis** — daily rollups via `features.admin_dashboard.rollups` (aggregates only)
- **Row history** — buffered gzipped JSONL via `features.history` (raw events for later debugging)

Row history is off by default (`backend: none`). When enabled, events buffer in memory and flush on **max records**, **max bytes**, a **time interval** (default 5 minutes), and **graceful shutdown** (SIGTERM/SIGINT). Rate-limit history archives **blocked requests only**; allowed traffic stays in memory + Redis aggregates.

Config lives under `features.history` in YAML:

```yaml
features:
  history:
    backend: s3            # none | local | s3
    role: sidecar          # sidecar | global (baked into filenames)
    streams: [cost, pii, usage, ratelimit]
    max_records: 1000
    max_bytes: 8388608
    max_age_seconds: 300
    gzip: true
    local:
      dir: logs/history
    s3:
      bucket: instawork-llm-proxy-history
      prefix: llm-proxy
      region: us-east-1
```

Object keys are partition-friendly and multi-writer safe:

`s3://<bucket>/<prefix>/<stream>/dt=YYYY-MM-DD/hour=HH/<ts>-<role>-<instanceID>-<seq>-<rand>.jsonl.gz`

`instanceID` resolves from `HISTORY_INSTANCE_ID`, then `HOSTNAME`, then the host name (sanitized).

#### Athena (JSON SerDe)

One external table per stream. Example for cost events:

```sql
CREATE EXTERNAL TABLE llm_proxy_cost (
  timestamp string, provider string, model string, endpoint string,
  input_tokens int, output_tokens int, total_tokens int, total_cost double,
  user_id string, request_id string
)
PARTITIONED BY (dt string, hour string)
ROW FORMAT SERDE 'org.openx.data.jsonserde.JsonSerDe'
LOCATION 's3://instawork-llm-proxy-history/llm-proxy/cost/'
TBLPROPERTIES (
  'projection.enabled'='true',
  'projection.dt.type'='date', 'projection.dt.format'='yyyy-MM-dd',
  'projection.dt.range'='2026-01-01,NOW', 'projection.dt.interval'='1','projection.dt.interval.unit'='DAYS',
  'projection.hour.type'='integer', 'projection.hour.range'='0,23', 'projection.hour.digits'='2',
  'storage.location.template'='s3://instawork-llm-proxy-history/llm-proxy/cost/dt=${dt}/hour=${hour}/'
);
```

Repeat for `pii`, `usage`, and `ratelimit` streams by changing the `LOCATION` and `storage.location.template` path segment. Query `role` and `instanceID` from the `"$path"` pseudo-column when needed.

### Middleware

- **Logging**: Logs all incoming requests with streaming detection
- **CORS**: Adds CORS headers for browser compatibility
- **Streaming**: Optimized handling for streaming responses
- **Error Handling**: Provider-specific error handling

### Adding New Providers

To add a new provider:

1. Create a new file (e.g., `newprovider.go`)
2. Implement the `Provider` interface
3. Add streaming detection logic
4. Add response metadata parsing
5. Create corresponding test file
6. Register the provider in `main.go`

**Passthrough providers.** If your upstream uses request signing (SigV4, mTLS
client certs, etc.) rather than a static API key, implement the Provider in
"passthrough mode": leave `ValidateAPIKey` as a no-op, never mutate the body
or signed headers, and only strip your own URL prefix. The Bedrock provider
([`internal/providers/bedrock.go`](internal/providers/bedrock.go)) is the
reference implementation — it also demonstrates how to plumb response-model
attribution when the response body does not echo back a model name (the
middleware falls back to `ExtractRequestModelAndMessages`, which pulls the
model from the request URL).

## Dependencies

- [Gorilla Mux](https://github.com/gorilla/mux) - HTTP router and URL matcher

## Build Information

The binary includes build-time information:

- Git commit hash
- Build timestamp
- Go version

View build info with:

```bash
make version
```
