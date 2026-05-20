---
name: llm-proxy-pr-review
description: Review the current branch of the `llm-proxy` Go service for issues likely introduced by AI/LLMs — duplicate code, placeholders, ignored errors, goroutine leaks, missing `ctx` propagation, mock-only tests, provider/middleware/circuit-breaker regressions, and config/test drift. Use when the user says "llm-proxy pr review", "review my llm-proxy branch", "review this pr", "ai smell review", or "/llm-proxy-pr-review".
---

# llm-proxy-pr-review

Review the current branch for issues likely introduced by AI/LLMs. Output only **negatives** — no positives, no pre-existing issues unrelated to this PR.

This skill is the Go/`llm-proxy` analogue of Finch's `/finch-pr-review`. The structure is the same (fan out to readonly subagents, aggregate, emit a severity-ordered list), but the anti-patterns are Go- and proxy-specific.

## 1. Determine the diff scope

Run these first and cache the output for the subagents:

```bash
git rev-parse --abbrev-ref HEAD
git status --porcelain
git diff --stat
```

Then compute the review diff:

- If current branch is `main` or `master`: review staged + unstaged changes only.
  - Working diff = `git diff HEAD`
- Otherwise: review the full branch vs. the base plus any staged/unstaged changes.
  - Base = `$(git merge-base HEAD origin/main 2>/dev/null || git merge-base HEAD origin/master)`
  - Branch diff = `git diff "$BASE"...HEAD`
  - Working diff = `git diff HEAD`

Collect the union of changed files from the branch diff and working diff:

```bash
# Only when reviewing a non-main branch with BASE set:
git diff --name-only "$BASE"...HEAD

# Always include local staged/unstaged changes:
git diff --name-only HEAD
```

Bucket the union (Go layout — tests live next to the code they test as `*_test.go`):

- `go_files`: `**/*.go` (excluding `*_test.go`)
- `go_test_files`: `**/*_test.go`
- `config_files`: `configs/*.yml`, `configs/*.yaml`
- `infra_files`: `Dockerfile*`, `docker-compose*.yml`, `Makefile`, `build/**`, `scripts/**`, `.github/**`, `go.mod`, `go.sum`
- `other_files`: docs, markdown, examples, requirements.txt, anything else

Pass the branch diff and working diff separately to each reviewer so committed branch changes and local staged/unstaged changes are both visible.

## 2. Fan out to parallel review subagents

In a **single message**, launch the following subagent reviewers in parallel. Use `readonly: true`. Do **not** pass an explicit `model` unless the user requested one and it is currently available. Pass each subagent the exact list of files it owns, the branch diff, and the working diff. Each subagent should return a JSON list of findings with `{file, lines, severity, category, description, suggested_fix}`.

### Subagent A — General AI-smell pass (all changed files)

Use `subagent_type: "ai-smell-reviewer"` with the full changed-file list and focus area `llm-proxy PR review: AI-smell, duplicate code, placeholders, dead code, swallowed errors, and mock-only tests`.

- Duplicate code across files/blocks that should be extracted into a shared function or helper in `internal/providers/test_helpers.go`, `internal/middleware/`, or similar
- Long inline blocks (>80 lines in one function) that should be refactored into named helpers
- `TODO` / `FIXME` / `panic("not implemented")` / `panic("TODO")` placeholders where a real implementation is expected
- Dead code, unused exported symbols, unreachable branches introduced by the diff (anything `go vet` / `staticcheck` would catch)
- Tests that don't exercise non-test code paths (pure mock-on-mock). Mocks/fakes are OK, but **at least one real production method must be executed per test**. Flag tests that duplicate production logic inside the test body instead of importing it.
- Overly broad error swallowing: `if err != nil { return nil }`, `_ = something()` on a call that returns `error`, `defer resp.Body.Close()` without checking err on `Close()` for write paths, recovered panics that discard the error
- Re-implementations of utilities that already exist in `internal/providers/test_helpers.go`, `internal/config/`, `internal/circuit/`, or `internal/middleware/`

### Subagent B — Go correctness pass (`go_files`)

Use `subagent_type: "explore"` with thoroughness `"medium"`.

- **Ignored errors.** Functions returning `(T, error)` whose `error` is discarded with `_` or by re-using a different name. Especially on `json.Unmarshal`, `io.Copy`, `req.Body.Close()`, `resp.Body.Close()` (write side), `tx.Commit()`, redis/DynamoDB ops.
- **Context propagation.** Network/DB/redis/dynamodb/HTTP calls that take a `context.Context` but are passed `context.Background()` or `context.TODO()` where the surrounding function already has a `ctx context.Context` available. Same for goroutines spawned inside a request handler that don't carry the request `ctx`.
- **Goroutine leaks.** `go func() {…}` started without a way to stop (no `ctx`, no `done` channel, no `sync.WaitGroup`); goroutines that do network IO with no timeout; goroutines that send to a channel nobody reads.
- **HTTP clients without timeouts.** `&http.Client{}` literals with no `Timeout` and no `Transport.ResponseHeaderTimeout`. Reverse proxies in `internal/providers/` must have an explicit timeout strategy (or document why none).
- **`defer` inside a loop.** Resource leak; the deferred call only fires when the function returns. Flag `defer rows.Close()`, `defer f.Close()`, `defer resp.Body.Close()` inside `for`/`for range`.
- **Mutex misuse.** Passing `sync.Mutex` / `sync.RWMutex` by value, copying a struct that embeds one, locking without a corresponding unlock on every return path (use `defer mu.Unlock()`).
- **Map access from multiple goroutines without sync.** Look at `internal/circuit/memory.go`, `internal/ratelimit/memory*.go`, `internal/cost/*.go` for new shared state — must be guarded by `sync.Mutex`, `sync.RWMutex`, or `sync.Map`.
- **Channel misuse.** Send on a `nil` channel, send on a `close`d channel, closing a channel from the receiver side, closing the same channel twice.
- **`panic` / `log.Fatal` / `os.Exit` outside `main`.** Library packages under `internal/` must return errors. Only `cmd/llm-proxy/`, `cmd/llm-proxy-keys/`, and `cmd/config-validator/` may exit the process.
- **`interface{}` / `any` where a concrete type is known.** Especially in new exported function signatures and struct fields. Generics or concrete types are preferred.
- **String concatenation for SQL / Redis keys / URLs.** Look for `fmt.Sprintf` building queries or untrusted URL paths instead of parameterized APIs / `url.URL`.
- **`time.Now()` baked into business logic instead of an injectable clock.** New code in `internal/circuit/`, `internal/ratelimit/`, `internal/cost/` should accept a `func() time.Time` or a clock interface so tests can advance time.
- **Hardcoded `time.Sleep` in production code paths** (acceptable in benchmarks/integration; never in request handling).
- **Missing `context.Context` as the first argument** of a new exported function that does I/O.
- **Slice / map mutation aliasing.** Returning a sub-slice of an internal buffer; mutating a map passed by the caller without documenting it.

#### Project-specific Go regressions

- **Provider interface drift.** Changes to `internal/providers/provider.go` (`Provider` interface) without updating every implementer: `openai.go`, `anthropic.go`, `gemini.go`, `bedrock.go`. Method signatures and `LLMResponseMetadata` fields must stay aligned.
- **Streaming SSE handlers** in `internal/providers/*.go` and `internal/middleware/streaming.go` that:
  - Do not call `Flush()` on the `http.Flusher` after each chunk
  - Do not propagate upstream `ctx.Done()` to close the client connection
  - Mutate response bodies in ways that change `Content-Length` without recomputing it
- **Middleware ordering changes** in the proxy wiring (request lifecycle: API key validation → rate limit → cost → logging → testmode → provider). Flag any change that re-orders these so that, e.g., logging runs before API key validation, or rate-limit happens before key resolution.
- **Circuit-breaker store interface drift.** `internal/circuit/store.go` is the abstract interface; `memory.go` and `redis.go` must both satisfy it identically. Flag a behavior change in one store that isn't mirrored in the other (see `internal/circuit/redis_behavior_test.go` for the parity contract).
- **Rate-limit store drift.** Same parity rule for `internal/ratelimit/memory.go` vs `internal/ratelimit/redis.go` — they must behave identically for the same input.
- **Cost tracker writes** in `internal/cost/` that block the request path. DynamoDB writes must be best-effort/async; flag any new synchronous DynamoDB call on the hot proxy path.
- **API key validation bypass.** New routes that skip `internal/middleware/apikey_validation.go` without being explicitly health-check / metadata endpoints.
- **Config schema changes.** New fields in `internal/config/config.go` (`ModelPricing`, rate limits, etc.) that aren't reflected in `configs/base.yml` or aren't validated by `cmd/config-validator/`.

### Subagent C — Config + infra pass (`config_files` + `infra_files`)

Use `subagent_type: "explore"`.

- **`configs/*.yml` edits without a matching test or validator run.** Any pricing, alias, or rate-limit edit must be exercised by `internal/config/...` tests and `cmd/config-validator/`.
- **Tiered pricing collapsed to flat pricing**, or new flagship model added without copying limits from a sibling (see `llm-price-update` skill for the contract).
- **Aliases across version numbers.** `claude-opus-4-0` aliased to `claude-opus-4-1`, `gpt-5` aliased to `gpt-5.1`, `gemini-2.5-pro` aliased to `gemini-3-pro`, etc. Different version numbers = separate entries.
- **Dockerfile / docker-compose** changes that drop a healthcheck, change the entrypoint without updating both `build/Dockerfile` and `build/Dockerfile.prod`, or bake credentials into the image.
- **`Makefile` targets** added without being listed in the `help` block.
- **`go.mod` upgrades** that pull in a major-version bump without verifying via `go test ./...` and `go vet ./...`.
- **New env vars** referenced in code but not documented in `README.md`, `Makefile env-check`, or `docker-compose.yml`.
- **Production vs dev/staging config drift** — pricing must live in `base.yml` only; flag duplicated pricing blocks in `dev.yml` / `staging.yml` / `production.yml`.

### Subagent D — Tests correctness (`go_test_files`)

Use `subagent_type: "explore"`.

- Tests that pass trivially (e.g., asserting a mock was called with the value the test itself set, asserting `len(x) >= 0`).
- Tests whose only "real" code path is constants or JSON marshalling — they don't call any production function under review.
- Missing assertions after arrange/act (a `t.Run` with no `t.Errorf` / `require.*` after the call).
- `t.Fatal` / `t.Fatalf` called from inside a goroutine — not goroutine-safe; must use `t.Errorf` and signal via channel.
- `httptest.NewServer(...)` / `httptest.NewTLSServer(...)` without a deferred `srv.Close()`.
- `time.Sleep` used to wait for async work instead of `sync.WaitGroup`, channel, or `eventually`-style polling. These hide races and slow CI.
- Hardcoded ports / hostnames / file paths instead of `httptest.Server.URL`, `t.TempDir()`, or `:0`.
- Tests that mutate package-level globals (e.g., `time.Now` shim, env vars) without restoring them in a `t.Cleanup`.
- Subtests (`t.Run`) that share mutable state without `t.Parallel()` discipline, or that call `t.Parallel()` while sharing a non-thread-safe fixture.
- **Parity coverage gaps.** Memory vs Redis store changes in `internal/circuit/` or `internal/ratelimit/` must add or extend a parity test (the file pattern is `*_behavior_test.go` for circuit). Provider behavior changes must extend `internal/providers/*_test.go` for every affected provider.
- **Generated table data**: a test that diffs against a hardcoded expected blob must regenerate the blob if the production output legitimately changed, not patch the test to weaken the assertion.
- Race detector hygiene: new goroutines / shared state under test should be covered by a test that would fail under `go test -race ./...`.

## 3. Aggregate

After all subagents return:

1. Merge findings, de-duplicate by `(file, lines, description)`.
2. Group by severity: `blocker` → `major` → `minor` → `nit`.
3. Within each severity, group by file.
4. For each finding show: `path:line` • one-line description • one-line suggested fix.
5. End with a short **Counts** line: `blocker=N major=N minor=N nit=N`.

If there are **no** findings, output exactly: `No AI-smell issues found in this diff.`

## 4. Optional verification commands

These are not mandatory for the review itself but are useful for the user as next steps. Reference them in the suggested fixes when relevant, do not run them as part of the review pass:

```bash
go vet ./cmd/... ./internal/...
go test ./internal/... -short -skip "Integration"
go test -race ./internal/...
go run ./cmd/config-validator/   # required after any configs/*.yml edit
```

## Rules

- Do not comment on positives.
- Do not flag pre-existing code untouched by this PR.
- Do not suggest stylistic preferences not backed by a concrete bug, a Go best practice (effective Go / `go vet` / `staticcheck`), or a project convention visible in the existing code.
- Prefer citing file paths with `path:line` so they're clickable.
- Severity guidance:
  - `blocker` — would break production, leak goroutines, swallow errors silently, corrupt pricing/rate-limit state, or cause data race.
  - `major` — incorrect behavior under realistic inputs, missing test coverage for a new code path, provider/middleware drift.
  - `minor` — quality issue (duplicate code, unused symbol, ignored err on a low-stakes path).
  - `nit` — stylistic only; keep these rare.
