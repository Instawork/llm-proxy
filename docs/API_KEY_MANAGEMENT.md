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
  - `daily_cost_limit`: 24-hour cost limit in cents (`0` = unlimited)
  - `monthly_cost_limit`: calendar-month cost limit in cents (`0` = unlimited)
  - `owner_email`: Owner email for personal keys (empty for org-wide keys)
  - `description`: Optional key description
  - `enabled`: Whether the key is active
  - `created_at`, `updated_at`: Timestamps
  - `expires_at`: Optional expiration date
  - `tags`: Key-value tags for organization

### Global secondary index: `OwnerProviderIndex`

Personal (viewer-owned) keys are indexed by owner and provider:

- **Partition key**: `owner_email`
- **Sort key**: `provider`

Dev/test tables created by the proxy include this GSI automatically. **Existing staging and production tables** must have the index added before deploying viewer personal keys:

```bash
aws dynamodb update-table \
  --table-name llm-proxy-api-keys \
  --attribute-definitions \
    AttributeName=owner_email,AttributeType=S \
    AttributeName=provider,AttributeType=S \
  --global-secondary-index-updates \
    '[{"Create":{"IndexName":"OwnerProviderIndex","KeySchema":[{"AttributeName":"owner_email","KeyType":"HASH"},{"AttributeName":"provider","KeyType":"RANGE"}],"Projection":{"ProjectionType":"ALL"}}}]'
```

Wait for the index to reach `ACTIVE` before rolling out the feature.

## Viewer personal keys

Dashboard users with the **viewer** role can manage personal proxy keys only:

- **One key per provider** (`openai`, `anthropic`, `gemini`; Bedrock is not available)
- **Monthly spend cap** (default **$20/month**, configured via `admin_dashboard.viewer_limits.personal_monthly_cost_limit_cents` in `configs/base.yml`)
- **No daily cap** on personal keys (`daily_cost_limit` is `0`)
- Personal keys cannot be renamed; the name is fixed at creation and mirrors the upstream credential name. Editors and admins can still change their spend limit, PII setting, rate limits, expiry, and status
- Viewers see the **API Keys** page only; monitoring routes require **editor** or **admin**
- Viewers may create share links and delete keys they own; org-wide keys remain editor/admin scoped
- Key routes (`GET`/`PATCH`/`DELETE /admin/api/keys/{key}`, `/stats`, and `POST /admin/api/share`) return `404` for any key the caller may not access, so a key's existence cannot be probed
- Share links are capability URLs: anyone holding the link (signed in or not, any role) can resolve it
- Revoking a share link (`DELETE /admin/api/share/{id}`) requires access to the shared key or having created the link; otherwise the route returns `404`

Legacy keys with an empty `owner_email` are unchanged and remain visible to editors and admins only.

## Service key viewers

An editor or admin can give individual users **read-only** access to a single org-wide key without changing anyone's role. Access is per key, not per user.

- The key record stores a `viewer_emails` string set. Emails are lowercased; each key holds at most 50 viewers. Personal keys cannot have viewers.
- The **requester** of a key (`requester_email`, set when a key request is approved) is an implicit viewer and cannot be removed. This also makes the "View" link on an approved request resolve for the requester.
- Viewers see the key in the same table as their own keys on the API Keys page (marked **Shared**, read-only, with month-to-date spend), under **Your keys** on Overview, and on a read-only detail page. Overview has a scope filter: **My spend** (default, keys the viewer owns), **Shared key spend** (keys shared with them) and **All spend**. It is sent as `GET /admin/api/spend-overview?scope=mine|shared|all` (viewers only; unknown values return 400; editors and admins always get the fleet view) and drives the totals, provider breakdown, history chart and unmetered count. Every visible key is still listed once, and rows outside the selected view are marked as not included in the total. The choice is kept in the page URL (`?scope=`). A key with no traffic yet still shows its limits and $0.00 spend, with setup guidance in a collapsible panel below. The response contains the masked key ID, provider, description, enabled state, daily/monthly cost limits, created/expiry dates, and spend (today, this month, cap, 7-day history). It never contains the usable key, the upstream (`actual_key`) credential, owner or requester email, rate-limit overrides, tags, PII settings, or PII/rate-limit telemetry.
- Viewers cannot mutate: `PATCH`, `DELETE`, and share-link creation on a key they can read return `403`. A key a user has no access to still returns `404`, so existence cannot be probed.
- Editors and admins manage viewers from the key detail page (**Viewers** panel) or the API:
  - `GET /admin/api/keys/{key}/viewers`
  - `POST /admin/api/keys/{key}/viewers` with `{"emails": ["a@example.com"]}` (emails must be in the allowed domain)
  - `DELETE /admin/api/keys/{key}/viewers/{email}`
- CLI (writes go through the same production guard as other mutating commands; `viewers add` enforces the same allowed domain as the API: `LLM_PROXY_ADMIN_ALLOWED_DOMAIN`, then `admin_dashboard.allowed_domain`, then `example.com`):

```bash
./llm-proxy-keys viewers add    -key sk-iw-... -email a@example.com,b@example.com
./llm-proxy-keys viewers list   -key sk-iw-...
./llm-proxy-keys viewers remove -key sk-iw-... -email a@example.com
./llm-proxy-keys -show=sk-iw-...   # also prints the Viewers line
```

No new DynamoDB index is required. Viewer additions and removals use atomic `ADD`/`DELETE` set updates, so concurrent edits do not overwrite each other. A viewer's shared-key rows (key list and Overview) are built by scanning the keys table and filtering in the service, the same cost profile as the editor key list; add a GSI only if the table grows large enough for that scan to matter.

## Key expiry

Any key (org-wide or personal) can have an optional expiry date, set from the admin UI (presets: 1 day, 7 days, 30 days, 90 days, or a custom date) or via the API's `expires_at` field. `expires_at` is stored as an absolute RFC3339 UTC timestamp.

- **Enforcement is immediate**: once `expires_at` is in the past, every request using that key is rejected at the validation path (`Store.GetKey`) with `API key has expired`, the same way a disabled key is rejected. No separate mechanism is needed for this part.
- **Cleanup runs on a delay**: a background sweeper (`internal/keyexpiry`) periodically finds keys that expired more than `grace_period_days` ago (default 7) and retires them — revoking the upstream provider credential first if the key was provisioned (`internal/provision`), then deleting the DynamoDB record. The grace period keeps an expired key visible (marked "Expired") in the admin UI for a few days before it disappears.
- **Editing**: expiry can be set, extended, shortened, or cleared at any time via `PATCH /admin/api/keys/{key}` with `expires_at` set to an ISO timestamp (set/extend) or `null` (clear). Viewers may set expiry when creating their own personal key, but cannot edit it afterward — only editors and admins can PATCH `expires_at`.
- **Bounds**: `expires_at` must be in the future and no more than `max_days` (default 365) out, enforced on both create and update.

Configure the sweeper under `features.api_key_management.expiry` in YAML:

```yaml
features:
  api_key_management:
    expiry:
      enabled: true
      sweep_interval_seconds: 900  # how often the sweeper runs (default 900 = 15 min)
      grace_period_days: 7         # delay between expiry and hard delete (default 7)
      max_days: 365                # furthest an expiry date can be set (default 365)
```

Only one ECS task performs cleanup at a time: the sweeper acquires a lease via a conditional put on a fixed DynamoDB record (`lease:key-expiry-sweep`) before scanning for expired keys, so running multiple proxy instances does not cause duplicate revoke calls or delete races.

## Key requests from integrations

A ticketing system (for example Init, which handles `#it-helpdesk`) can file an org-key request on a user's behalf, poll its status, and cancel it. An admin still reviews it in the dashboard, and the requester still reveals the key themselves with their own SSO session. The integration never sees the key.

### Configuration

Integrations are configured under `features.admin_dashboard.integrations`. They are off in `base.yml` and every deployed environment; `dev.yml` enables a `dev` caller. When `enabled` is false the routes are not mounted, so they return `404`.

```yaml
features:
  admin_dashboard:
    integrations:
      enabled: true
      requests_per_minute: 60             # per caller token bucket (burst = this value)
      max_daily_cost_limit_cents: 10000   # cap on integration-filed requests, at filing and at approval
      max_monthly_cost_limit_cents: 100000
      ticket_url_hosts: "${LLM_PROXY_INTEGRATION_TICKET_URL_HOSTS}"  # comma-separated, exact host match
      callers:
        - id: init                        # becomes the request's source
          token_sha256_env: LLM_PROXY_INTEGRATION_INIT_TOKEN_SHA256
          scopes: [key_requests:create, key_requests:read, key_requests:cancel]
```

A caller whose `token_sha256_env` variable is unset or malformed is skipped with a warning; the other callers keep working.

### Tokens

Tokens look like `llmpi_<caller>_<random>`. Mint one with the key tool (it needs no AWS access or config):

```bash
go run ./cmd/llm-proxy-keys integration-token new -caller init
```

It prints the token once and its SHA-256. Give the token to the integration through a secrets manager and put only the SHA-256 in the proxy's `token_sha256_env` variable. The proxy compares the SHA-256 of each presented token against every configured hash in constant time and never logs the token or the hash.

To rotate, set the variable to `<new-sha256>,<old-sha256>`, switch the integration to the new token, then remove the old hash.

### Endpoints

All requests send `Authorization: Bearer llmpi_...`. Session cookies are not accepted here, and the bearer token is not accepted on any session route.

| Route | Scope | Result |
| --- | --- | --- |
| `POST /admin/api/integrations/key-requests` | `key_requests:create` | `201` new request, `200` the existing request for the same `external_ticket_id`, `409` the requester already has a pending request for that provider |
| `GET /admin/api/integrations/key-requests/{id}` | `key_requests:read` | `200` status |
| `POST /admin/api/integrations/key-requests/{id}/cancel` | `key_requests:cancel` | `200` cancelled, `409` no longer pending |

A missing or unknown token returns `401`, a missing scope `403`, and an exhausted rate limit `429` with `Retry-After`. A request filed by another caller, or from the dashboard, returns `404`.

The create body:

```json
{
  "requester_email": "person@example.com",
  "provider": "openai",
  "name": "widget-bot",
  "description": "Key for the widget bot",
  "justification": "optional, up to 2000 characters, shown only to admins",
  "daily_cost_limit": 1500,
  "monthly_cost_limit": 50000,
  "external_ticket_id": "INIT-1234",
  "external_ticket_url": "https://app.init.inc/tickets/INIT-1234"
}
```

- `requester_email` must be in the allowed domain, and `provider` must support auto-provisioning.
- `description` is required (up to 500 characters). `name` must contain a letter or number.
- `external_ticket_id` is required and must match `^[A-Za-z0-9._:-]{1,64}$`. Retrying with the same ticket ID is safe: it returns `200` with the original request and does not email admins again.
- `external_ticket_url` is optional. It must be `https` on one of the `ticket_url_hosts`; if none are configured, any URL is rejected.
- A daily limit of `0` uses the key-request default, clamped to `max_daily_cost_limit_cents`. A monthly limit of `0` uses `max_monthly_cost_limit_cents`. Limits above the integration maxima (or the editor daily maximum) are rejected. When an admin approves an integration request, the same maxima apply to any override, and the monthly cap can't be removed.
- The body is capped at 16 KiB.

The response carries `id`, `status` (`pending`, `approved`, `rejected`, `cancelled`), `provider`, `name`, the limits, `external_ticket_id`, `created_at`, `reviewed_at`, `rejection_reason`, `review_url` (the dashboard link for admins), `claimed`/`claimed_at`, and, once approved, `claim_url` for the requester to open. It never contains the key, any email address, or the justification.

The admin notification email for a new request includes the ticket ID and the source (`Requested via: init`) and links to `<public_base_url>/admin/keys?tab=requests&request_id=<id>`.

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

### Backfill personal key monthly limits

After raising `admin_dashboard.viewer_limits.personal_monthly_cost_limit_cents` in config, existing personal keys keep their stored `monthly_cost_limit` until updated. To bump keys still at the old default ($10 → $20):

```bash
# SSO into production, then dry-run (lists matching keys only)
export AWS_PROFILE=your-prod-sso-profile
aws sso login --profile "$AWS_PROFILE"
./scripts/bump-personal-key-limits.sh

# Apply (only keys with monthly_cost_limit exactly at the old default)
LLM_PROXY_ALLOW_PROD=1 ./scripts/bump-personal-key-limits.sh --apply
```

Or without the wrapper:

```bash
go run ./cmd/llm-proxy-keys personal bump-limit -env=production
LLM_PROXY_ALLOW_PROD=1 go run ./cmd/llm-proxy-keys personal bump-limit -env=production --apply
```

Only personal keys whose `monthly_cost_limit` equals `--from` (default 1000 cents) are updated to `--to` (default 2000 cents). Custom limits are left unchanged.

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
curl "https://proxy.example.com/gemini/v1/models/gemini-pro:generateContent" \
  -H "x-goog-api-key: YOUR_PROXY_KEY" \
  -H "Content-Type: application/json" \
  -d '{"contents": [...]}'
```

Proxy keys are only accepted in headers (`Authorization: Bearer`, `x-api-key`,
or `x-goog-api-key`). A proxy key in the `?key=` query string is rejected with
401, because the URL is written to every access log between the client and the
proxy.

The one exception is Gemini: every `/gemini/*` route still accepts a proxy key
in `?key=`. n8n's Google Gemini(PaLM) credential is query-string auth only and
sends the key as `?key=` on every call it makes through that credential — the
model list, `generateContent`, and uploads alike — so the exemption covers the
whole provider rather than one endpoint.

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
