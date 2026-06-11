#!/usr/bin/env bash
# Idempotent deps for share-box snippet smoke tests (node, python venv, integration go.mod).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SNIPPETS="${ROOT}/snippets"
NODE_DIR="${SNIPPETS}/node"
PY_DIR="${SNIPPETS}/python"
REQ="${PY_DIR}/requirements.txt"

log() { echo "… install-deps: $*" >&2; }

file_hash() {
  shasum -a 256 "$1" | awk '{print $1}'
}

combined_hash() {
  local combined=""
  local f
  for f in "$@"; do
    combined+="$(file_hash "$f")"
  done
  printf '%s' "$combined" | shasum -a 256 | awk '{print $1}'
}

need_reinstall() {
  local marker="$1"
  shift
  local f
  for f in "$@"; do
    if [[ ! -f "$f" ]]; then
      return 0
    fi
  done
  if [[ ! -f "$marker" ]]; then
    return 0
  fi
  local want
  want="$(combined_hash "$@")"
  [[ "$(cat "$marker")" != "$want" ]]
}

write_marker() {
  local marker="$1"
  shift
  combined_hash "$@" >"$marker"
}

install_node() {
  if ! command -v npm >/dev/null 2>&1; then
    log "npm not found — node snippets will skip in live tests"
    return 0
  fi
  local pkg="${NODE_DIR}/package.json"
  local lock="${NODE_DIR}/package-lock.json"
  local marker="${NODE_DIR}/.deps-hash"
  if [[ ! -f "$pkg" ]]; then
    log "missing ${pkg}"
    return 1
  fi
  local inputs=("$pkg")
  if [[ -f "$lock" ]]; then
    inputs+=("$lock")
  fi
  if [[ ! -d "${NODE_DIR}/node_modules" ]] || need_reinstall "$marker" "${inputs[@]}"; then
    log "npm install in ${NODE_DIR}"
    (cd "$NODE_DIR" && npm install --no-fund --no-audit)
    write_marker "$marker" "${inputs[@]}"
    log "node deps ready"
  else
    log "node deps up to date (${NODE_DIR}/node_modules)"
  fi
}

install_python() {
  local py=""
  for candidate in python3 python; do
    if command -v "$candidate" >/dev/null 2>&1; then
      py="$candidate"
      break
    fi
  done
  if [[ -z "$py" ]]; then
    log "python3 not found — python snippets will skip in live tests"
    return 0
  fi
  if [[ ! -f "$REQ" ]]; then
    log "missing ${REQ}"
    return 1
  fi
  local venv="${PY_DIR}/.venv"
  local venv_py="${venv}/bin/python3"
  local marker="${PY_DIR}/.deps-hash"
  if [[ ! -x "$venv_py" ]] || need_reinstall "$marker" "$REQ"; then
    if [[ ! -x "$venv_py" ]]; then
      log "creating venv ${venv}"
      "$py" -m venv "$venv"
    fi
    log "pip install -r ${REQ}"
    "$venv_py" -m pip install -r "$REQ" --quiet
    write_marker "$marker" "$REQ"
    log "python deps ready (${venv_py})"
  else
    log "python deps up to date (${venv})"
  fi
}

install_go() {
  log "go mod download (${ROOT})"
  (cd "$ROOT" && go mod download)
  log "go module deps ready"
}

log "snippet deps check (root=${ROOT})"
install_node
install_python
install_go
log "all snippet deps ok"
