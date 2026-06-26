---
name: format-lint-generate
description: Format, lint, vet, validate config, and test the `llm-proxy` Go service. Runs `gofmt -s`, `gofumpt`, `go vet`, the config validator, and the race-enabled unit test suite — the same checks CI's `lint` and `unit-tests` jobs enforce. Use when the user says "format", "lint", "check", "gofmt", "gofumpt", "go vet", "run checks", "pre-commit checks", "CI checks", "/format-lint-generate", or pastes the "Go code is not formatted:" CI failure.
---

# Format, Lint & Generate (llm-proxy)

Run all formatting, linting, vetting, config validation, and tests for the `llm-proxy` Go service.
Commands run from the repository root (the directory containing `Makefile` and `go.mod`).

Default to the `Makefile` targets (`make ci`, `make fmt-strict`, `make vet`, `make check`, `make test`) where they exist, and use the raw CI commands (`gofmt -s -l .`, `gofumpt -l .`) only for the cases the Makefile does not cover — most notably the **`gofmt -s` simplify** and **gofumpt stricter format** checks that CI enforces but `make fmt` does not run.

**Quick start:** from the repo root, run `make ci` (or `./scripts/ci-check.sh`) before pushing. To auto-fix formatting first: `make ci-fix`.

Before deciding whether to install tooling or regenerate anything, inspect the changed files (`git status --short`, `git diff --stat`). Do not start the server just for these checks.

## What CI actually enforces

The `.circleci/config.yml` `lint` job fails the build if any of these fail:

1. `go vet ./...`
2. `gofmt -s -l .` — must produce **zero** lines
3. `gofumpt -l .` — must produce **zero** lines

And the `unit-tests` job runs:

1. `go run ./cmd/config-validator/`
2. `make test` — which is `go test -race -v ./internal/... -short -skip "Integration"`

A "passes locally" check sequence must cover **all five**. `make check` (which is `fmt vet lint`) is necessary but **not sufficient** — it does not run `gofmt -s` (only `go fmt`, which is non-simplifying), does not run `gofumpt`, and does not run the config validator. Always run the explicit CI commands when reproducing a CI failure.

## 1. Go: Format & Fix

Apply both formatters, in this order, on the whole repo. Running both is necessary because `gofmt -s` and `gofumpt` can disagree on column-aligned single-line method definitions; running `gofumpt` last and then `gofmt -s` again on any flagged file converges quickly.

```bash
# 1a. gofmt with the simplify (-s) flag CI uses
gofmt -s -w .

# 1b. gofumpt (stricter than gofmt). Install once if missing.
command -v gofumpt >/dev/null || go install mvdan.cc/gofumpt@latest
PATH="$(go env GOPATH)/bin:$PATH" gofumpt -w .
```

If `gofumpt` and `gofmt -s` disagree on a specific file (you'll see one rewrite undo the other), the usual culprit is a tight column-aligned group of method declarations like:

```go
func (f *fakeProvider) Short(req *http.Request) string { return "" }
func (f *fakeProvider) Mid(r *mux.Router)              {}
func (f *fakeProvider) Long(req *http.Request, ks providers.APIKeyStore) error { return nil }
```

Break the alignment by inserting blank lines between unrelated method definitions, or by promoting the long one to a multi-line body. Then re-run both formatters.

`make fmt` runs `go fmt ./cmd/... ./internal/...`, which is **not** equivalent — it omits `-s` and `gofumpt` — so do not substitute it for the CI checks above.

## 2. Go: Lint, Vet & Config Validation

```bash
# Vet — same as CI
go vet ./...

# Stricter golint style gate (deprecated upstream but pinned in Makefile)
make lint

# Config validator — required after any configs/*.yml edit
go run ./cmd/config-validator/
```

`make check` runs `fmt vet lint` together but, as noted, uses non-simplifying `go fmt`. Prefer the explicit commands above when reproducing CI.

## 3. Format & Lint Verification (CI parity)

After fixing files, re-run the exact checks CI runs and confirm they print nothing:

```bash
gofmt -s -l . || true
PATH="$(go env GOPATH)/bin:$PATH" gofumpt -l . || true
go vet ./...
```

`gofmt -s -l` and `gofumpt -l` exit 0 even when they have findings, so check that their output is empty, not just the exit code:

```bash
if [ -n "$(gofmt -s -l .)" ]; then echo "gofmt would change files"; gofmt -s -l .; exit 1; fi
if [ -n "$(PATH="$(go env GOPATH)/bin:$PATH" gofumpt -l .)" ]; then echo "gofumpt would change files"; PATH="$(go env GOPATH)/bin:$PATH" gofumpt -l .; exit 1; fi
```

## 4. Tests

The Makefile's test targets all enable `-race`, which is mandatory — concurrency bugs in the rate limiter, cost tracker, and circuit breaker can only be observed deterministically with the race detector on.

```bash
# Unit tests, race-enabled, integration skipped (CI's unit-tests job)
make test

# Targeted run, race-enabled, equivalent to make test for a single package
go test -race ./internal/circuit/... -short -skip Integration

# Integration tests (requires OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY)
make test-integration
```

Do **not** drop `-race` when re-running a subset; a green run without it is not sufficient evidence to merge a change that touches `internal/circuit/`, `internal/ratelimit/`, `internal/cost/`, `internal/providers/`, or any goroutine-spawning code path.

## 5. Generate / Update

This service has no generated client to refresh, but two "generation-like" steps occasionally apply. Only run them when their inputs have actually changed (check `git diff`):

```bash
# After editing go.mod or pulling new dependencies
go mod tidy
make install

# After editing configs/*.yml — this is the "did my config change compile" gate
go run ./cmd/config-validator/
```

Pricing/model changes to `configs/base.yml` have their own dedicated playbook — see the `llm-price-update` skill.

## Quick Reference: Full Check Sequence

For a clean "ready to push" pass, run:

```bash
make ci
```

Or, to fix formatting then verify:

```bash
make ci-fix
```

`make ci` runs, in order: `fmt-check`, `vet`, `lint-pii-logs`, `validate-config`, `test`.

For extra local coverage (fuzz unit tests + web typecheck/tests):

```bash
make ci-extended
```

Manual step checklist (equivalent to `make ci`):

```
Task Progress:
- [ ] gofmt -s -l . is empty          (or: make fmt-check)
- [ ] gofumpt -l . is empty           (or: make fmt-check)
- [ ] go vet ./...                    (or: make vet)
- [ ] make lint-pii-logs
- [ ] go run ./cmd/config-validator/  (or: make validate-config)
- [ ] make test                       (race-enabled unit tests)
- [ ] go mod tidy                     (only if go.mod / go.sum changed)
```

## Reproducing the CI "Go code is not formatted" failure

If CI's `lint` job prints something like:

```
Go code is not formatted:
internal/circuit/transport.go
internal/middleware/ratelimit_test.go
...
Exited with code exit status 1
```

That output comes from CI's `gofmt -s -l .` step. To fix:

1. `gofmt -s -w .` then `gofumpt -w .` on the whole repo (don't try to fix one file at a time — `gofumpt` may flag unrelated files that have been pre-existing CI escapees).
2. Check the result with `gofmt -s -l .` and `gofumpt -l .` — both must be empty.
3. If only the listed files were touched in your PR, restrict the write step to those files to avoid pulling in unrelated formatting drift:

   ```bash
   gofmt -s -w path/to/file1.go path/to/file2.go ...
   PATH="$(go env GOPATH)/bin:$PATH" gofumpt -w path/to/file1.go path/to/file2.go ...
   ```

4. If `gofmt -s` and `gofumpt` keep flipping the same file, fix the underlying alignment ambiguity as described in section 1.
