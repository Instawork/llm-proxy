#!/bin/bash

# Safety guards. Without `set -euo pipefail` a typo on any aws sub-command
# silently flows through and "✅ Table created successfully!" prints even
# after an upstream failure. With pipefail in place, any non-zero exit
# inside a pipeline aborts the script.
set -euo pipefail

TABLE_NAME="${TABLE_NAME:-llm-proxy-cost-tracking}"
REGION="${AWS_REGION:-us-west-2}"

# Hard refuse to run against the AWS production account by default.
# Operators may override with LLM_PROXY_ALLOW_PROD=1 when intentionally
# bootstrapping a fresh prod region. Without this guard a developer who
# mistakenly exported a production AWS profile would create the
# cost-tracking table (with PAY_PER_REQUEST billing) in the wrong
# account on first run.
#
# The production account ID is *not* hard-coded here so this script can
# be committed publicly without leaking infra coordinates. Set
# LLM_PROXY_PROD_AWS_ACCOUNT_ID in your shell / CI context (e.g. via the
# same secret store that holds AWS_ECR_REGISTRY_ID) to activate the
# guard. If unset, the guard is a no-op and only the explicit
# LLM_PROXY_ALLOW_PROD=1 acknowledgement gate applies.
AWS_ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text 2>/dev/null || echo unknown)"
PROD_AWS_ACCOUNT_ID="${LLM_PROXY_PROD_AWS_ACCOUNT_ID:-}"
if [[ -n "${PROD_AWS_ACCOUNT_ID}" \
    && "${AWS_ACCOUNT_ID}" == "${PROD_AWS_ACCOUNT_ID}" \
    && "${LLM_PROXY_ALLOW_PROD:-0}" != "1" ]]; then
    echo "❌ Refusing to run against the production AWS account (${AWS_ACCOUNT_ID})."
    echo "   Set LLM_PROXY_ALLOW_PROD=1 to override."
    exit 1
fi

echo "🚀 Setting up DynamoDB table: $TABLE_NAME in region: $REGION (account: ${AWS_ACCOUNT_ID})"

# Idempotency check: bail out cleanly if the table already exists.
# DescribeTable returns non-zero on missing-table, so we tolerate that
# and only abort on unexpected errors.
if aws dynamodb describe-table --table-name "$TABLE_NAME" --region "$REGION" >/dev/null 2>&1; then
    echo "ℹ️  Table $TABLE_NAME already exists in $REGION — nothing to do."
    exit 0
fi

echo "📋 Creating DynamoDB table..."
aws dynamodb create-table \
    --table-name "$TABLE_NAME" \
    --region "$REGION" \
    --attribute-definitions \
        'AttributeName=pk,AttributeType=S' \
        'AttributeName=sk,AttributeType=S' \
        'AttributeName=gsi1pk,AttributeType=S' \
        'AttributeName=gsi1sk,AttributeType=S' \
        'AttributeName=gsi2pk,AttributeType=S' \
        'AttributeName=gsi2sk,AttributeType=S' \
        'AttributeName=gsi3pk,AttributeType=S' \
        'AttributeName=gsi3sk,AttributeType=S' \
    --key-schema \
        'AttributeName=pk,KeyType=HASH' \
        'AttributeName=sk,KeyType=RANGE' \
    --global-secondary-indexes \
        'IndexName=ProviderModelIndex,KeySchema=[{AttributeName=gsi1pk,KeyType=HASH},{AttributeName=gsi1sk,KeyType=RANGE}],Projection={ProjectionType=ALL}' \
        'IndexName=UserProviderIndex,KeySchema=[{AttributeName=gsi2pk,KeyType=HASH},{AttributeName=gsi2sk,KeyType=RANGE}],Projection={ProjectionType=ALL}' \
        'IndexName=ModelProviderIndex,KeySchema=[{AttributeName=gsi3pk,KeyType=HASH},{AttributeName=gsi3sk,KeyType=RANGE}],Projection={ProjectionType=ALL}' \
    --billing-mode PAY_PER_REQUEST

echo "⏳ Waiting for table to become active..."
aws dynamodb wait table-exists \
    --table-name "$TABLE_NAME" \
    --region "$REGION"

echo "🕒 Enabling TTL for automatic cleanup..."
if ! aws dynamodb update-time-to-live \
    --table-name "$TABLE_NAME" \
    --region "$REGION" \
    --time-to-live-specification \
        Enabled=true,AttributeName=ttl; then
    echo "⚠️  Warning: Failed to enable TTL, but table was created successfully."
fi

echo "✅ Table created successfully!"
echo "📊 Verifying table structure..."

aws dynamodb describe-table \
    --table-name "$TABLE_NAME" \
    --region "$REGION" \
    --query 'Table.[TableName,TableStatus,GlobalSecondaryIndexes[*].[IndexName,IndexStatus]]' \
    --output table

echo ""
echo "🎉 Setup complete! Your DynamoDB table is ready for cost tracking."
echo "📝 Next steps:"
echo "   1. Update your config.yml with the correct table name and region"
echo "   2. Ensure your AWS credentials have DynamoDB read/write permissions"
echo "   3. Test your LLM proxy to verify cost tracking works" 
