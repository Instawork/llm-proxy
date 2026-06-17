# POST /redact API

Standalone Presidio-backed text redaction for tools and Cursor hooks. Any valid
`iw-*` proxy API key can call it. Per-key request limits apply via
`features.redact_api.requests_per_minute` (independent of provider rate limiting).

## Enable

```yaml
features:
  pii_redact:
    analyzer_url: "http://localhost:3000"  # required when redact_api is on
  redact_api:
    enabled: true
    fail_mode: closed   # required; open is rejected at config validation
    requests_per_minute: 120  # per iw-* key; 0 = unlimited
```

`PRESIDIO_ANALYZER_URL` overrides `analyzer_url` at process start (ECS uses
`http://localhost:3000` in the same task).

## API

**`POST /redact`**

| | |
|---|---|
| Auth | `Authorization: Bearer <iw-key>` or `x-api-key: <iw-key>` |
| Dev auth | With `dev_allow_unauthenticated: true` in dev.yml (requires `dev_bypass_login`), no key is needed |
| Query | `mode=text` (default) or `mode=json` |
| Body (`text`) | `Content-Type: text/plain` — raw string |
| Body (`json`) | `Content-Type: application/json` — `{"text":"..."}` |

Redaction uses one-way `[REDACTED:TYPE]` markers (not wire placeholders).

### Responses

| mode | 200 body |
|------|----------|
| `text` | `text/plain` redacted string |
| `json` | `{"text":"…","entities":{"US_SSN":1},"changed":true}` |

| Status | Meaning |
|--------|---------|
| 400 | Invalid mode, empty body, or oversize |
| 401 | Missing/invalid/disabled key |
| 429 | Per-key `requests_per_minute` exceeded |
| 503 | Presidio failure (always fail-closed) |

## Key storage (Cursor hooks)

**Never commit `iw-*` secrets.** They are bearer tokens.

| Safe in git | Not safe in git |
|-------------|-----------------|
| Hook script (`.cursor/hooks/redact-prompt.sh.example`) | The `iw-…` key value |
| Env var name `LLM_PROXY_API_KEY` | `hooks.json` with embedded key |
| Public endpoint URL | SSM fetch on every hook invocation |

### Recommended setup

1. Mint a normal `iw-*` key in the admin dashboard (or reuse an existing one).
2. One-time bootstrap into your shell profile:

   ```bash
   export LLM_PROXY_API_KEY=iw-…
   # or from SSM (once per session / in .zshrc):
   export LLM_PROXY_API_KEY="$(aws ssm get-parameter --name /your/path --with-decryption --query Parameter.Value --output text)"
   export LLM_PROXY_URL=https://llm.instawork.com
   ```

3. Point a user-level `~/.cursor/hooks.json` at the hook script (see
   `.cursor/hooks/redact-prompt.sh.example`).

Hooks should read `$LLM_PROXY_API_KEY` only — not call AWS on every prompt.

## Local dev (no iw-* key)

1. From the repo: `make test-pii-up` (Presidio on `http://localhost:5004`).
2. Ensure docker-compose dev stack is running (`docker compose up -d`); `dev.yml` sets
   `redact_api.dev_allow_unauthenticated: true`.
3. Restart the proxy if you changed YAML (air usually picks it up).

```bash
curl -sf -X POST "http://localhost:9002/redact?mode=text" \
  -H "Content-Type: text/plain" \
  --data-binary "patient SSN 222-33-4444"
```

Optional live check:

```bash
cd integration && go run ./cmd/llm-proxy-live -suite redact
```

To test with a real `iw-*` key instead, set `LLM_PROXY_REDACT_KEY` or pass
`Authorization: Bearer iw-…`.

## Example

```bash
curl -sf -X POST "${LLM_PROXY_URL}/redact?mode=text" \
  -H "Authorization: Bearer ${LLM_PROXY_API_KEY}" \
  -H "Content-Type: text/plain" \
  --data-binary "SSN 123-45-6789"
```

## Cursor hook

Copy `.cursor/hooks/redact-prompt.sh.example` to `~/.cursor/hooks/redact-prompt.sh`,
`chmod +x`, and register under `beforeSubmitPrompt` in `~/.cursor/hooks.json` with
`failClosed: true`.

Recognizers match [`internal/redact/redactor.go`](../internal/redact/redactor.go)
(`DefaultEntityTypes`).
