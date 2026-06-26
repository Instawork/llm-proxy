#!/usr/bin/env bash
set -euo pipefail

# Bump existing personal API keys from the old default monthly cap ($10) to the
# new default ($20) in DynamoDB. New keys pick up the limit from configs/base.yml
# after deploy; this script backfills keys already stored at the old default.
#
# Prerequisites:
#   aws sso login --profile <your-prod-profile>
#   export AWS_PROFILE=<your-prod-profile>
#
# Usage:
#   ./scripts/bump-personal-key-limits.sh              # dry-run
#   LLM_PROXY_ALLOW_PROD=1 ./scripts/bump-personal-key-limits.sh --apply
#
# Optional env:
#   LLM_PROXY_PROD_AWS_ACCOUNT_ID  — when set, refuses prod account unless
#                                    LLM_PROXY_ALLOW_PROD=1 (same as setup-dynamodb.sh)

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if ! aws sts get-caller-identity >/dev/null 2>&1; then
  echo "AWS credentials are not available. Run: aws sso login --profile <profile>" >&2
  exit 1
fi

ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text)"
echo "AWS account: ${ACCOUNT_ID} (profile: ${AWS_PROFILE:-default})"
echo ""

if [[ " $* " != *" --apply "* ]]; then
  echo "Dry run — pass --apply to write changes"
  echo ""
fi

exec go run ./cmd/llm-proxy-keys personal bump-limit -env=production "$@"
