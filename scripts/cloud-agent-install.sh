#!/usr/bin/env bash
# Pre-warm the llm-proxy toolchain for Cloud Agent runs (vendor-audit
# automation, llm-price-update). Idempotent; safe to re-run.
#
#   bash scripts/cloud-agent-install.sh
#
# Referenced from .cursor/environment.json here and, guarded on the sibling
# checkout existing, from the finch environment for the multi-repo workspace.
set -euo pipefail

cd "$(dirname "$0")/.."

log() { printf 'cloud-agent-install: %s\n' "$*"; }

if ! command -v go >/dev/null 2>&1; then
  log "go not on PATH; skipping Go warm-up"
else
  log "go mod download"
  go mod download
  if ! command -v gofumpt >/dev/null 2>&1 && [ ! -x "$(go env GOPATH)/bin/gofumpt" ]; then
    log "go install mvdan.cc/gofumpt@latest"
    go install mvdan.cc/gofumpt@latest
  fi
fi

if command -v python3 >/dev/null 2>&1; then
  log "pip install -r requirements.txt (jsonschema for scripts/validate-audit.py)"
  python3 -m pip install -q --user -r requirements.txt 2>/dev/null \
    || python3 -m pip install -q -r requirements.txt
fi

log "ok"
