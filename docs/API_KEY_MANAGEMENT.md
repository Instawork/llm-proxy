# API Key Management Feature

## Overview

The LLM Proxy now supports a custom API key management system that allows you to:

- Generate proxy-specific API keys prefixed with `sk-<base>-` (default `sk-iw-`)
- Store actual provider keys securely in DynamoDB
- Set 24-hour cost limits per key (for future implementation)
- Enable/disable keys without deleting them
- Track key usage and metadata

## How It Works

1. **Key Generation**: Generate unique keys prefixed with `sk-<base>-` (configure `<base>` via `key_prefix` in YAML, default `iw`)
2. **Key Storage**: Actual provider API keys are stored securely in DynamoDB
3. **Key Validation**: When a request comes in with a proxy key, the proxy:
   - Looks up the key in DynamoDB
   - Validates it's enabled and not expired
   - Replaces it with the actual provider key
   - Forwards the request to the provider
4. **Pass-through**: Regular provider API keys (not recognized as proxy keys) pass through unchanged

## Configuration

Add the following to your environment configuration file (`dev.yml`, `staging.yml`, or `production.yml`):

```yaml
features:
  api_key_management:
    enabled: true
    table_name: "llm-proxy-api-keys"  # DynamoDB table name
    region: "us-west-2"                # AWS region
    key_prefix: "iw"                   # new keys: sk-iw-<hex>
```

## DynamoDB Table

The system automatically creates a DynamoDB table with the following structure:

- **Primary Key**: `pk` (the proxy key, e.g. `sk-iw-<hex>`)
- **Attributes**:
  - `provider`: LLM provider (openai, anthropic, gemini)
  - `actual_key`: The real provider API key
  - `daily_cost_limit`: 24-hour cost limit in cents
  - `description`: Optional key description
  - `enabled`: Whether the key is active
  - `created_at`, `updated_at`: Timestamps
  - `expires_at`: Optional expiration date
  - `tags`: Key-value tags for organization

## Using the Key Management Tool

### Building the Tool

```bash
go build ./cmd/llm-proxy-keys
```

### Creating a New Key

```bash
./llm-proxy-keys \
  -env=production \
  -provider=openai \
  -key="YOUR_OPENAI_API_KEY" \
  -desc="Production API key for service X" \
  -cost-limit=50000  # $500/day limit
```

Output:

```
✅ API Key Created Successfully!

Key:         sk-iw-<hex>
Provider:    openai
Description: Production API key for service X
Cost Limit:  $500.00/day
Created:     2024-01-15T10:30:00Z

🔑 Use this key in your API requests by replacing your provider key with the generated sk-iw-<hex> value
```

### Listing Keys

```bash
# List all keys
./llm-proxy-keys -env=production -list

# List keys for a specific provider
./llm-proxy-keys -env=production -list -provider=openai
```

### Managing Keys

```bash
# Show key details
./llm-proxy-keys -env=production -show=YOUR_PROXY_KEY

# Disable a key (temporarily)
./llm-proxy-keys -env=production -disable=YOUR_PROXY_KEY

# Enable a key
./llm-proxy-keys -env=production -enable=YOUR_PROXY_KEY

# Delete a key (permanent)
./llm-proxy-keys -env=production -delete=YOUR_PROXY_KEY
```

## Using Proxy Keys in API Requests

In the examples below, `YOUR_PROXY_KEY` stands in for the generated proxy key (format: `sk-iw-<hex>` by default).

### OpenAI Example

Instead of:

```bash
curl https://proxy.example.com/openai/v1/chat/completions \
  -H "Authorization: Bearer YOUR_OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [...]}'
```

Use:

```bash
curl https://proxy.example.com/openai/v1/chat/completions \
  -H "Authorization: Bearer YOUR_PROXY_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4", "messages": [...]}'
```

### Anthropic Example

Instead of:

```bash
curl https://proxy.example.com/anthropic/v1/messages \
  -H "x-api-key: YOUR_ANTHROPIC_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "claude-3-opus", "messages": [...]}'
```

Use:

```bash
curl https://proxy.example.com/anthropic/v1/messages \
  -H "x-api-key: YOUR_PROXY_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "claude-3-opus", "messages": [...]}'
```

### Gemini Example

Instead of:

```bash
curl "https://proxy.example.com/gemini/v1/models/gemini-pro:generateContent?key=YOUR_GEMINI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"contents": [...]}'
```

Use:

```bash
curl "https://proxy.example.com/gemini/v1/models/gemini-pro:generateContent?key=YOUR_PROXY_KEY" \
  -H "Content-Type: application/json" \
  -d '{"contents": [...]}'
```

## Security Considerations

1. **Key Storage**: Actual provider keys are stored encrypted in DynamoDB
2. **Key Transmission**: Always use HTTPS when sending API keys
3. **Key Rotation**: Regularly rotate both proxy and provider keys
4. **Access Control**: Use AWS IAM to restrict access to the DynamoDB table
5. **Monitoring**: Monitor key usage through CloudWatch and proxy logs

## Error Handling

When an invalid or disabled proxy key is used, the proxy returns:

```json
{
  "error": "Invalid API key: API key not found"
}
```

With HTTP status code 401 (Unauthorized).

## Future Enhancements

- **Cost Limiting**: The `daily_cost_limit` field is ready for implementing 24-hour spending limits
- **Usage Analytics**: Track usage per key for billing and monitoring
- **Key Expiration**: Automatic key expiration based on `expires_at` field
- **Rate Limiting**: Per-key rate limiting
- **Multi-provider Keys**: Single proxy key that works across multiple providers

## AWS Permissions Required

The proxy needs the following DynamoDB permissions:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "dynamodb:GetItem",
        "dynamodb:PutItem",
        "dynamodb:UpdateItem",
        "dynamodb:DeleteItem",
        "dynamodb:Query",
        "dynamodb:Scan",
        "dynamodb:DescribeTable",
        "dynamodb:CreateTable"
      ],
      "Resource": [
        "arn:aws:dynamodb:*:*:table/llm-proxy-api-keys*",
        "arn:aws:dynamodb:*:*:table/llm-proxy-api-keys*/index/*"
      ]
    }
  ]
}
```
