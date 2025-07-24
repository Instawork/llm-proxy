#!/bin/bash

# Configuration
TABLE_NAME="llm-proxy-cost-tracking"
REGION="us-west-2"

echo "🚀 Setting up DynamoDB table: $TABLE_NAME in region: $REGION"

# Create table using inline JSON
echo "📋 Creating DynamoDB table..."
aws dynamodb create-table \
    --table-name $TABLE_NAME \
    --region $REGION \
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

if [ $? -ne 0 ]; then
    echo "❌ Failed to create table. Check your AWS credentials and permissions."
    exit 1
fi

echo "⏳ Waiting for table to become active..."
aws dynamodb wait table-exists \
    --table-name $TABLE_NAME \
    --region $REGION

if [ $? -ne 0 ]; then
    echo "❌ Timeout waiting for table to become active."
    exit 1
fi

echo "🕒 Enabling TTL for automatic cleanup..."
aws dynamodb update-time-to-live \
    --table-name $TABLE_NAME \
    --region $REGION \
    --time-to-live-specification \
        Enabled=true,AttributeName=ttl

if [ $? -ne 0 ]; then
    echo "⚠️  Warning: Failed to enable TTL, but table was created successfully."
fi

echo "✅ Table created successfully!"
echo "📊 Verifying table structure..."

aws dynamodb describe-table \
    --table-name $TABLE_NAME \
    --region $REGION \
    --query 'Table.[TableName,TableStatus,GlobalSecondaryIndexes[*].[IndexName,IndexStatus]]' \
    --output table

echo ""
echo "🎉 Setup complete! Your DynamoDB table is ready for cost tracking."
echo "📝 Next steps:"
echo "   1. Update your config.yml with the correct table name and region"
echo "   2. Ensure your AWS credentials have DynamoDB read/write permissions"
echo "   3. Test your LLM proxy to verify cost tracking works" 
