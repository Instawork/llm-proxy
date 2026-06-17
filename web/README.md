# LLM Proxy Admin Dashboard (web)

Vite + React admin UI for the LLM Proxy. The production build is embedded into the Go binary and served under `/admin/`.

## Local development (Option A — Vite + Go API)

1. From the repo root, start the dev stack (includes the Vite dev server):

```bash
make docker-compose-up
# or: docker compose up -d
```

Docker Compose starts an in-memory **DynamoDB Local** sidecar for API key CRUD (`dynamodb` service on port 8000). Override with real AWS by unsetting `AWS_ENDPOINT_URL` and mounting `~/.aws` credentials.

2. `configs/dev.yml` enables the admin dashboard and **dev bypass login** (no Google OAuth required locally).
3. Open <http://localhost:5173/admin/> and click **Dev login (local session)**.

API calls go to `http://localhost:9002` with cookies. CORS is configured via `features.admin_dashboard.dev_cors_origin`.

Optional Google OAuth for local testing: set `LLM_PROXY_ADMIN_GOOGLE_CLIENT_ID`, `LLM_PROXY_ADMIN_GOOGLE_CLIENT_SECRET`, and `LLM_PROXY_ADMIN_OAUTH_REDIRECT_URL=http://localhost:9002/admin/auth/callback`.

Alternatively, run Vite on the host:

```bash
docker compose up llm-proxy
cd web && npm install && npm run dev
```

## Build

```bash
cd web
npm install
npm run build
```

`npm run check` runs TypeScript without emitting files.

## Go embed build tag

The Go server embeds `web/dist` only when built with `-tags embed_ui`:

```bash
# Production / release binary with UI baked in
go build -tags embed_ui ./cmd/llm-proxy

# Default local Go build (no embedded UI)
go build ./cmd/llm-proxy
```

- `web/embed.go` (`embed_ui`): embeds `dist/` via `//go:embed all:dist`
- `web/embed_stub.go` (`!embed_ui`): returns an empty `fs.FS`

A placeholder `web/dist/index.html` is checked in so `go:embed` succeeds before the first `npm run build`. Replace it by running `npm run build` before release builds with `embed_ui`.

## Routes

| Path | Description |
|------|-------------|
| `/admin/` | Dashboard (health, rate limits, config) |
| `/admin/keys` | API key management |
| `/admin/login` | SPA login redirect to `/admin/auth/login` |
| `/admin/auth/*` | Google OAuth (Go server) |
| `/admin/api/*` | JSON admin API (Go server) |
