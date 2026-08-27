# AGENTS.md

Guidance for AI coding agents working on `llm-proxy`. See [README.md](README.md)
for the full human-facing documentation and
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the system deep-dive (data
flow, package map, middleware order, key decisions).

## Project overview

A Go reverse proxy that forwards requests to LLM providers (OpenAI, Anthropic,
Gemini, AWS Bedrock) with streaming support, cost tracking, rate limiting, PII
redaction, a circuit breaker, and an embedded React admin dashboard. Built on
Gorilla Mux; module path is `github.com/Instawork/llm-proxy` (Go 1.24).

## Repository layout

- `cmd/llm-proxy/` — server entrypoint; other `cmd/` dirs are CLIs
  (`config-validator`, `llm-proxy-keys`, `llm-proxy-users`, lint helpers)
- `internal/` — all application code: `providers/` (per-provider proxy logic),
  `middleware/`, `circuit/`, `ratelimit/`, `cost/`, `pii/`, `config/`,
  `admin/`, plus per-domain `*stats/` recorders
- `configs/` — layered YAML config: `base.yml` merged with
  `{ENVIRONMENT}.yml` (`dev`, `staging`, `production`, `fuzz`, …) and an
  optional `LLM_PROXY_CONFIG_PROFILE` overlay (e.g. `sidecar.yml`)
- `web/` — Vite/React admin dashboard, embedded into the Go binary
- `integration/` — fuzz scenarios and live integration tooling
- `docs/` — [ARCHITECTURE.md](docs/ARCHITECTURE.md) plus feature deep-dives
  (API key management, PII redaction, redact API)

## Setup and run

```bash
make install build     # deps + binary
make dev               # run locally (default port 9002)
make docker-compose-up # full dev stack: proxy + redis + dynamodb + web UI
```

## Checks to run before pushing

`make ci` runs the same gates as CI (format check, vet, PII log lint, config
validation, race-enabled unit tests). To auto-fix formatting first, run
`make ci-fix`.

CI enforces, exactly:

1. `go vet ./...`
2. `gofmt -s -l .` — must output nothing (note the `-s`; plain `go fmt` is not enough)
3. `gofumpt -l .` — must output nothing (stricter than gofmt)
4. `go run ./cmd/config-validator/` — required after any `configs/*.yml` edit
5. `make test` — `go test -race ./internal/... -short -skip Integration`

Never drop `-race` when re-running a test subset: concurrency bugs in
`internal/circuit/`, `internal/ratelimit/`, and `internal/cost/` only surface
deterministically with the race detector on.

## Testing

- Unit tests: `make test` (race-enabled, skips integration). Coverage report:
  `make test-cover`.
- Integration tests: `make test-integration` — requires `OPENAI_API_KEY`,
  `ANTHROPIC_API_KEY`, `GEMINI_API_KEY` in the environment; `make env-check`
  verifies them. Don't run these unless asked.
- Fuzz scenarios (`make fuzz`, `make fuzz-all`) need a proxy running in the
  `fuzz` environment overlay; see the README's "Fake upstream mode" section.
- Web dashboard: `make web-check` (typecheck) and `make web-test`.

## Code style

- Formatting is `gofmt -s` **and** `gofumpt`; run both, gofumpt last. If they
  fight over a file, break up column-aligned single-line method definitions.
- Follow the existing provider pattern when adding a provider: implement the
  `Provider` interface in `internal/providers/`, register it in the server
  setup, and add a matching test file. `bedrock.go` is the reference for
  signed-request (passthrough) providers; see "Adding a provider" in
  [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
- Providers with request signing (SigV4) must never mutate the body, path, or
  signed headers — only strip the URL prefix.
- Config changes go through `configs/base.yml` plus the relevant environment
  overlay, and must pass `go run ./cmd/config-validator/`.
- Never log request/response bodies or PII; `make lint-pii-logs` gates this.

## Security considerations

- The proxy is credential pass-through: it forwards client API keys and SigV4
  signatures verbatim and must never store, log, or inspect them.
- Fake upstream mode and circuit-breaker test mode are gated behind
  `LLM_PROXY_ALLOW_FAKE_MODE` / `LLM_PROXY_ALLOW_TEST_MODE` env vars and must
  stay off in production configs.
