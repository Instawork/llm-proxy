# PII redaction

The proxy can call a Microsoft Presidio analyzer sidecar to scrub PII from
request bodies. By default (`wire_placeholders: true`), scrubbed placeholder
text is sent to the upstream LLM and MASK-tier values are restored in
responses to the client. Set `wire_placeholders: false` for legacy
observability-only mode (upstream sees the original body; only logs/cost
transports read the redacted copy).

The audited recognizer scope lives in code at
[`internal/redact/redactor.go`][1] (`DefaultEntityTypes`). Anything not
on that list is filtered out at construction so a YAML edit can never
extend the wire payload past the in-code allowlist — see
"Allowlist enforcement" below.

[1]: ../internal/redact/redactor.go

## Status

Off by default. The middleware is gated by
`features.pii_redact.enabled` in YAML — until you flip it, no /analyze
calls happen and no body capture overhead is incurred.

## Enabling locally

```bash
# 1. Bring up the analyzer sidecar (image is ~8 GB, slow first start).
docker compose --profile pii_redact up -d presidio

# 2. Edit configs/dev.yml to flip the flag:
#    features:
#      pii_redact:
#        enabled: true
#        analyzer_url: "http://presidio:3000"
#        fail_mode: "open"

# 3. Restart the proxy.
docker compose up llm-proxy
```

`PRESIDIO_ANALYZER_URL` overrides the YAML at process start so production
ECS task definitions can point at `localhost` in the same task without
rewriting YAML per env.

## Configuration reference

| Field             | Type      | Default       | Notes                                                                                         |
| ----------------- | --------- | ------------- | --------------------------------------------------------------------------------------------- |
| `enabled`         | `bool`    | `false`       | Master gate. When false, no other field matters.                                              |
| `analyzer_url`    | `string`  | required      | Base URL of the Presidio analyzer sidecar.                                                    |
| `fail_mode`       | `enum`    | `open`        | `open` = log warn + pass through unredacted; `closed` = abort 503.                            |
| `timeout_ms`      | `int`     | `3000`        | Per-/analyze deadline. When PII redaction is on, prefer a generous budget so redaction succeeds; tune down only after baselining warm sidecar p99. |
| `score_threshold` | `float64` | `0.5`         | Min Presidio confidence for a span to be redacted.                                            |
| `entity_types`    | `[]string`| `DefaultEntityTypes` | Recognizer scope. Narrow it to save latency. **Cannot be widened via YAML** — `redact.New` filters out anything that isn't on the in-code allowlist (with a `WARN` log). |
| `language`        | `string`  | `en`          | Forwarded as the `language` parameter to /analyze.                                            |
| `max_body_bytes`  | `int`     | `1048576` (1 MiB) | Bodies larger than this skip the redactor with a WARN log (tagged path / provider / body_bytes / max_body_bytes) and pass through to the upstream LLM intact (no redacted copy in context). 0 inherits the default. |
| `wire_placeholders` | `bool` | `true` | When true, scrubbed text (placeholders) is sent upstream; MASK values are restored in responses. |
| `default_allow_streaming` | `bool` | `true` | When false globally (or per-key via `allow_streaming: false`), wire-mode requests rewrite `"stream": false` so responses can be buffered. Streaming responses otherwise use delta restore with a carry buffer. |

## Policy tiers

| Tier | Replacement | Upstream LLM | Client response |
| ---- | ----------- | ------------ | --------------- |
| **MASK** | `<PERSON_1>` | placeholder | restored to original |
| **SEAL** | `<US_SSN_1>` | placeholder | stays opaque |
| **REDACT** | `[REDACTED:TYPE]` | marker | marker (no restore) |

Tier mapping lives in [`internal/redact/policy.go`](../internal/redact/policy.go)
and mirrors the comments in [`recognizers.yaml`](../recognizers.yaml).

## How requests flow

```
client ─▶ llm-proxy ──▶ MetaURLRewriting
                  │
                  ├──▶ APIKeyValidation
                  ├──▶ PIIRedactMiddleware  ◀──── Presidio /analyze
                  │       (scrubs body; wire mode replaces r.Body
                  │        with placeholders + stashes Registry)
                  │
                  ├──▶ Logging / RateLimit / CORS
                  ├──▶ TokenParsing  ──▶ cost transports (PIIRedactedBody)
                  ├──▶ PIIResponseRestore  (MASK restore on response)
                  ├──▶ Streaming
                  └──▶ provider proxy ─▶ upstream LLM
```

Downstream consumers (cost transports, structured loggers) opt in by
calling `middleware.PIIRedactedBody(r.Context())`. Response restore is
automatic when wire mode stashes a Registry — clients see restored MASK
values without changing their SDK code.

## Allowlist enforcement

`redact.DefaultEntityTypes` is the audited list of recognizers the
proxy is permitted to run. It deliberately excludes the noisy
defaults that ship with the prebuilt analyzer image — `UK_NHS`,
`DATE_TIME`, `MAC_ADDRESS`, `CRYPTO`, `MEDICAL_LICENSE`, `URL`, `NRP`,
and others — because they fire false positives on routine application
data (10-digit numeric IDs, ISO timestamps, etc.).

`redact.New` enforces this allowlist at construction:

- An empty `entity_types` falls back to `DefaultEntityTypes`.
- A non-empty `entity_types` is intersected with `DefaultEntityTypes`.
  Anything else is dropped with a `WARN` log naming the rejected
  entries.
- If the intersection is empty (e.g. someone wrote
  `entity_types: [UK_NHS, MEDICAL_LICENSE]`), the redactor falls back
  to the full `DefaultEntityTypes` rather than calling `/analyze` with
  empty `entities` — which Presidio interprets as "run ALL default
  recognizers", strictly worse than the documented default.

Widening the scope therefore requires editing
`internal/redact/redactor.go` and getting the change reviewed; it cannot
be done from YAML alone.

## Fail modes

- **fail_mode: open** (default, recommended for first rollout)

  Sidecar timeouts and 5xx responses are logged at WARN and the request
  passes through with no redacted body in context. The upstream LLM
  still serves the user; only the persisted copy may be unredacted for
  that single request.

- **fail_mode: closed**

  The same failure responds 503. Pick this only when the regulatory
  cost of a single unredacted log line outweighs availability.

## Logging audit

The PII redactor is a band-aid if other parts of the proxy are
already dumping bodies into stdout. We swept the codebase as part of
adding the feature; the findings are below.

### Fixed in this rollout

| Location                                                       | Was                                  | Now                                                       |
| -------------------------------------------------------------- | ------------------------------------ | --------------------------------------------------------- |
| `internal/providers/openai.go` (ParseResponseMetadata)         | Logged 100 raw bytes of every response | Routed through `redact.LogPreview`                        |
| `internal/providers/openai.go` (ParseResponsesAPIMetadata)     | Logged 100 raw bytes of every response | Routed through `redact.LogPreview`                        |
| `internal/providers/anthropic.go` (ParseResponseMetadata)      | Logged 100 raw bytes of every response | Routed through `redact.LogPreview`                        |
| `internal/middleware/token_parsing.go` (parse-error preview)   | Logged 200 raw bytes (gzip + plain) on metadata-parse failure | Routed through `redact.LogPreview`                        |

`redact.LogPreview` returns:

- the redacted excerpt if a global redactor is configured (which
  happens automatically in `main.go` when `pii_redact.enabled: true`), or
- a length-only summary `[len=N bytes; pii_redact disabled]` otherwise.

Either way, raw bytes never make it into the log.

### Confirmed safe

- **Cost transports** (`internal/cost/`). They consume only
  `LLMResponseMetadata` (token counts, model, request ID, latency).
  No body content reaches the file/DynamoDB/Datadog transports.

- **`LoggingMiddleware`** (`internal/middleware/logging.go`). Logs only
  method, path, remote_addr, duration. No body or headers.

- **Streaming middleware** (`internal/middleware/streaming.go`). Never
  logs chunk content — only a "ResponseWriter does not support flushing"
  warning.

- **Error-path logs** that include "body" (e.g. "Error reading request
  body for streaming check: %v"). They include only the error message,
  not the body content itself.

## Testing against a live sidecar

Unit tests use an `httptest`-backed fake analyzer. To prove the wire
contract against the real Microsoft image, there is a `--pii`
integration suite under `internal/redact/integration_test.go` and
`internal/middleware/pii_redact_integration_test.go`.

```bash
# 1. Bring up the sidecar (spaCy models are slow to load — first run
#    can take several minutes).
make test-pii-up

# 2. Run the integration suite. The Makefile target sets
#    LLM_PROXY_PII_INTEGRATION=1 and threads PRESIDIO_PORT through to
#    PRESIDIO_ANALYZER_URL.
make test-pii

# 3. Stop the sidecar when done.
make test-pii-down
```

The tests skip with an actionable message in three cases:

- `-short` is passed → "Skipping --pii integration test in -short mode".
- `LLM_PROXY_PII_INTEGRATION=1` is not set → tells the developer the env
  var to flip and the compose profile to bring up.
- The TCP dial to the analyzer URL fails → tells the developer to run
  `docker compose --profile pii_redact up -d presidio`.

That last gate prevents a CI run that hasn't stood up the sidecar from
silently flipping every assertion to "pass" by way of skip. If you want
to run the suite against a Presidio that's already up on a non-default
port (e.g. one already running on `localhost:5003`), pass
`PRESIDIO_PORT=5003 make test-pii` or set `PRESIDIO_ANALYZER_URL`
directly.

## Operational notes

### What `timeout_ms` does on a slow sidecar

`timeout_ms` is enforced via `context.WithTimeout` on every `/analyze`
call. When the deadline fires, `redact.Redact` returns a `context
deadline exceeded` error — and from there the middleware honours
`fail_mode`:

- `fail_mode: open` (default) → log WARN with the
  `error="context deadline exceeded"` field and pass the request
  through to the upstream LLM. Status 200, no redacted body in context.
  This trades a single unredacted log line for availability.

- `fail_mode: closed` → respond **503** to the client immediately and
  never invoke upstream. Pick this once you've baselined sidecar uptime
  and want regulatory hardness over availability for that single
  request.

Both paths are unit-tested against a deadline-exceeded error
(`TestPIIRedactMiddleware_TimeoutFailOpen…` and
`TestPIIRedactMiddleware_TimeoutFailClosed…`) and exercised end-to-end
against a real sidecar with the `--pii` integration suite. Rolling out
in `open` first and graduating to `closed` once Datadog shows a stable
analyzer p95 is the recommended path.

Watch the `pii_redact: ok` info lines for the `duration` field —
that's the per-request analyzer round trip. Once warm p99 is well below
`timeout_ms`, you can tune the timeout downward if added latency matters.

### What the body-size cap does in practice

The middleware reads `MaxBodyBytes` from `pii_redact.max_body_bytes` in
YAML (default **1 MiB** when unset or zero). Bodies above the cap:

1. **Pass through to the upstream LLM intact.** No truncation, no
   modification. The middleware always restores `r.Body` from the
   captured bytes regardless of redaction outcome.

2. **Are NOT sent to the analyzer sidecar.** A WARN log fires with
   enough structured fields to alert on and tune the cap:
   ```
   pii_redact: body exceeds max_body_bytes; skipping path=/openai/v1/chat/completions provider=openai body_bytes=1340544 max_body_bytes=1048576
   ```
   The `body_bytes` / `max_body_bytes` pair tells you the overshoot
   ratio, and `provider` lets per-provider Datadog monitors fire
   independently.

3. **Leave nothing in `request.Context()` for downstream consumers.**
   Anything calling `middleware.PIIRedactedBody(ctx)` gets
   `(nil, false)` and falls back to its default behaviour (cost
   transports already only persist metadata; structured loggers that
   opted in skip the body fingerprint for that request).

What hits the 1 MiB default in real traffic:

| Request shape                                | Typical size   | Cap?           |
| -------------------------------------------- | -------------- | -------------- |
| Chat completion, single turn                 | 1 – 5 KB       | No             |
| Chat completion, long thread, many turns     | 30 – 80 KB     | No             |
| Embeddings, single input                     | 1 – 10 KB      | No             |
| Embeddings, batch of 100+ inputs             | 100 – 500 KB   | No             |
| Vision request with one base64 image         | 100 KB – 2 MB  | Sometimes      |
| Audio transcription, file uploads            | 1 – 25 MB      | **Always**     |

The bias of the 1 MiB default is "redact almost everything; only let
truly unusual uploads slip through." If the WARN counter still trends
upward in Datadog, raise `max_body_bytes` further — but baseline
analyzer latency first, since redacting a 5 MB body costs real CPU on
the sidecar.

#### Datadog query templates

```
# Count of skipped bodies, broken down by provider
sum:logs.pii_redact.oversize_skipped{*} by {provider}

# Histogram of overshoot ratio (body_bytes / max_body_bytes)
avg:logs.pii_redact.body_bytes{*} / avg:logs.pii_redact.max_body_bytes{*}
```

### Streaming responses

Never touched. The proxy is response-stream transparent by design. If
you need redacted SSE bodies for storage, redact at the point of
writing the storage record, not in the stream path.
