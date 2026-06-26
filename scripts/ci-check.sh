#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

usage() {
	cat <<'EOF'
Usage: scripts/ci-check.sh [target]

Run local CI checks from the llm-proxy repo root.

Targets (default: ci):
  ci          CircleCI lint + unit-tests parity (format, vet, config, tests)
  ci-fix      Apply gofmt -s/gofumpt then run ci
  ci-extended ci + fuzz-test + web typecheck/tests
  fmt-strict  Apply gofmt -s and gofumpt only
  fmt-check   Verify formatting without writing files

Examples:
  ./scripts/ci-check.sh
  ./scripts/ci-check.sh ci-fix
  make ci
EOF
}

target="${1:-ci}"

case "$target" in
	ci | ci-fix | ci-extended | fmt-strict | fmt-check | check)
		make "$target"
		;;
	-h | --help | help)
		usage
		;;
	*)
		echo "Unknown target: $target" >&2
		usage >&2
		exit 1
		;;
esac
