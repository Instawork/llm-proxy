# LLM Proxy Admin Dashboard (web)

Vite + React admin UI for the LLM Proxy. The production build is embedded into the Go binary and served under `/admin/`.

## Local development

1. Start the proxy with the admin dashboard enabled in config (`features.admin_dashboard.enabled: true`).
2. Set `features.admin_dashboard.dev_cors_origin` to `http://localhost:5173` so the Vite dev server can call the API.
3. Export OAuth/session env vars (see `internal/admin` docs in the main repo).
4. Install and run the UI:

```bash
cd web
npm install
npm run dev
```

Open http://localhost:5173/admin/ — API calls go to `http://localhost:9002` with cookies.

Alternatively, use docker compose from the repo root:

```bash
docker compose up web llm-proxy
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
