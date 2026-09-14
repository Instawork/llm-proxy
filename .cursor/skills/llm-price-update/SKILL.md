---
name: llm-price-update
description: Refresh LLM provider pricing (OpenAI, Anthropic, Google Gemini, AWS Bedrock) in configs/base.yml for the llm-proxy app and audit which vendor API endpoints the proxy meters, using the latest vendor audit file in audits/. Use when the user asks to "update llm prices", "refresh model pricing", "sync llm-proxy pricing", "check llm pricing", "llm price update", "model audit", "endpoint audit", "apply the audit", or otherwise wants the llm-proxy pricing config and endpoint coverage reconciled with the latest audits/audit-MM-DD-YYYY.json.
---

# llm-price-update

Reconcile model pricing in [configs/base.yml](../../../configs/base.yml) and endpoint coverage in [internal/providers/endpoints.go](../../../internal/providers/endpoints.go) with the latest vendor audit in [audits/](../../../audits/). Produce the pricing diff, a deprecation list, and an endpoint-coverage report.

The vendor research (reading pricing, deprecations, and API-reference pages for OpenAI, Anthropic, Google, and AWS Bedrock) is **not** done by this skill. It is done by the scheduled vendor-audit Cursor automation, which writes `audits/audit-MM-DD-YYYY.json` conforming to [audits/audit.schema.json](../../../audits/audit.schema.json). This skill only reads that file and applies it to the config. The same audit file drives the `model-audit` skill in the finch repo, so both services move in lockstep from one snapshot of vendor truth.

## Hard rules

1. **The audit file is the only source of vendor data.** Never fill a price, date, alias, or model name from model knowledge. If the latest audit lacks something you need, record it as an open question in the output and leave the config untouched for that model; do not look it up yourself and do not guess.
2. **Refuse a stale audit.** If the latest audit's `generated_at` is more than 14 days old, stop and tell the user to run the vendor-audit automation (Cursor dashboard → Automations) before continuing. Prices and sunset dates move weekly; applying an old snapshot can re-introduce a price the vendor already changed.
3. **Never add aliases for models that are actually different versions.** Aliases are only for the same underlying model (e.g., `claude-opus-4-1` ↔ `claude-opus-4-1-20250805`, `gpt-4o` ↔ `gpt-4o-2024-11-20`). Never alias `gpt-5` to `gpt-5.1`, `claude-opus-4-0` to `claude-opus-4-1`, `gemini-2.5-pro` to `gemini-3-pro`, etc. Different version numbers = different models = separate entries. The audit's `aliases` arrays follow the same rule; if one does not, treat it as an audit bug and flag it rather than copying it.
4. **Always encode tiered pricing when the audit has more than one tier.** This is not optional. If a model's `pricing` array has two or more tiers (Gemini Pro ≤200k vs >200k, GPT-5.6 ≤272k vs >272k, Anthropic Sonnet 4.x ≤200k vs >200k), the model MUST be configured as a `PricingTier` list — never collapsed to flat `{input, output}`. Flat pricing silently under-bills long-prompt traffic. A vendor sentence such as "prompts with >272K input tokens are priced at 2x input / 1.5x output" on a model card **is** a published tier even when the pricing table shows "–" for long context — encode it (and fix the audit row) whenever the model's context window exceeds the threshold.
5. **Encode list price, not promo price.** `promo_pricing` in the audit is recorded so you can leave a comment; the `pricing` block always gets the list rate so cost tracking never under-bills after the promo lapses.
6. **Always produce a deprecations list** alongside the pricing diff so it can be consumed by downstream callers (e.g., client UI, docs). See "Deprecation output" below.
7. **Always audit endpoint coverage.** A model whose price is right is still billed at $0 if the request path is not in the metered list of `internal/providers/endpoints.go`. Every run diffs the audit's `endpoints` arrays against that registry and checks live traffic for unmetered paths (see "Step 4b"). This is how the Gemini Interactions API ran unbilled for a day: the model was known, the endpoint was not.
8. **Never remove existing aliases.** An alias that the vendor no longer lists (or never listed) is harmless and may still be pinned by a client, a saved prompt, or an admin-dashboard row; dropping it breaks that caller's pricing lookup for no gain. Aliases only leave `configs/base.yml` when the whole model moves to `retired_models`, and then they move with it.

## Workflow

Copy this checklist at the start of a run:

```text
Task progress:
- [ ] 1. Read configs/base.yml to capture current pricing and models
- [ ] 2. Load the latest audits/audit-MM-DD-YYYY.json; stop if stale or invalid
- [ ] 3. Map audit fields onto base.yml shapes (tiers, aliases, retired_models)
- [ ] 4. Diff audit models against configs/base.yml (pricing + deprecations)
- [ ] 4a. Re-verify every non-no-op row at its `source` URL / AWS card; probe GetModelPricing for changed ids
- [ ] 4b. Diff audit endpoints against internal/providers/endpoints.go; check by_unmetered and Datadog for live unmetered traffic
- [ ] 5. Build pricing changeset + deprecations list + endpoint-coverage report
- [ ] 6. Present plan (or apply, depending on mode)
- [ ] 7. Run `go run ./cmd/config-validator/` after editing
- [ ] 8. Run `go test ./internal/config/...` after editing
```

### Step 1. Read the current config

Only `configs/base.yml` holds pricing. `dev.yml`, `staging.yml`, and `production.yml` inherit pricing from base and do not override it. Confirm with a grep before assuming:

```bash
rg -n "pricing" configs/
```

The pricing struct is defined in [internal/config/config.go](../../../internal/config/config.go) as `ModelPricing` with three shapes:
- `{input, output}` for flat pricing — ONLY when the vendor publishes a single rate for the model
- `[{threshold, input, output}, ...]` for prompt-length tiers — REQUIRED whenever the vendor publishes length-based tiers (Gemini Pro ≤200k/>200k, Gemini 1.5 ≤128k/>128k, Anthropic Sonnet 4.x ≤200k/>200k, etc.)
- `overrides: {alias: {input, output}}` for per-snapshot price differences (e.g., `gpt-4o-2024-05-13` priced higher than current `gpt-4o`)

Retired models live in the `retired_models:` block (provider → slug → `retired_date`, `replacement`, `aliases`); `LookupRetiredModel` serves them a clear error instead of an unpriced passthrough. Live models that the vendor has scheduled for shutdown carry `deprecated: true` and `replacement:` on the model entry.

### Step 2. Load the latest audit

Audit files are named `audits/audit-MM-DD-YYYY.json`; pick the newest by date, then validate it and check staleness:

```bash
latest=$(ls audits | rg '^audit-(\d{2})-(\d{2})-(\d{4})\.json$' -r '$3-$1-$2 $0' | sort | tail -1 | cut -d' ' -f2)
python3 scripts/validate-audit.py "audits/$latest"
jq -r '.generated_at, .previous_audit' "audits/$latest"
```

If validation fails, or `generated_at` is more than 14 days ago (rule 2), stop and report; the fix is a new automation run, not a hand edit of the audit.

Useful views of the file:

```bash
# every model with a status other than active/preview → the deprecations list
jq '[.providers | to_entries[] | .key as $p | .value.models[] | select(.status | IN("deprecated","shutdown","legacy")) | {provider: $p, id, status, sunset_date, replaced_by, source, notes}]' "audits/$latest"

# models the previous audit did not know about → new-model candidates
jq '[.providers | to_entries[] | .key as $p | .value.models[] | select(.new_since_previous_audit) | {provider: $p, id, aliases, pricing, release_date}]' "audits/$latest"

# billable endpoints per provider → input to step 4b
jq '.providers | map_values([.endpoints[] | select(.billable != "no") | {method, path, billable, streaming_usage_event}])' "audits/$latest"
```

### Step 3. Map audit fields onto base.yml

| Audit field | base.yml |
|---|---|
| `providers.<p>.models[].id` | model key under `providers.<p>.models`. Audit `gemini` = config `gemini`; audit `bedrock` covers both `bedrock` and `bedrock-mantle` (match on the model id, e.g. `us.anthropic.…` vs `anthropic.…`). |
| `aliases` | `aliases:` — union with the existing list (rule 8: never drop one) |
| `pricing` with one tier | `pricing: {input, output}` |
| `pricing` with N tiers | `pricing:` list; audit `up_to_tokens: X` → `threshold: X`, the final `up_to_tokens: null` tier → `threshold: 0` |
| `promo_pricing` | a `#` comment next to `pricing` noting the promo rate and `ends_on`; never the encoded price (rule 5) |
| `status: deprecated` / `legacy` with `sunset_date` | `deprecated: true` + `replacement: <replaced_by>` on the live entry, comment with the sunset date |
| `status: shutdown` | move the entry (and its aliases) to `retired_models.<p>` with `retired_date: <sunset_date>` and `replacement: <replaced_by>` |
| `verified_by` empty on a changed row | flag as an open question; apply only if the change is small (<20%) |

Anything the audit reports that has no home above (for example `capabilities`, `context_window`) is for the finch `model-audit` skill and is ignored here.

### Step 4. Build the diff

For each provider section of `configs/base.yml`, categorize every model:

| Category | Action |
|---|---|
| Unchanged | Skip |
| Price changed | Update `input`/`output` (or the tier list); if an alias has a different price, move it into `overrides` |
| New model (`new_since_previous_audit: true`, or in the audit but not in the config) | Add entry with canonical ID + same-model aliases only (see rule 3) |
| `status: deprecated`, future or unannounced sunset | Keep in config, set `deprecated: true` + `replacement:`, add to deprecations list with `sunset_date` |
| `status: shutdown` (past sunset) | Move to `retired_models`, add to deprecations list. Google retired `gemini-1.5-*` and `text-embedding-004`/`embedding-001` this way. |
| In the config but absent from the audit entirely | Open question — the vendor no longer publishes it, but the audit did not confirm a shutdown. Do not remove it. |

Reuse the `limits` block of a similar existing model rather than inventing new rate-limit numbers — e.g., a new flagship uses the same limits as the previous flagship.

Bedrock rows need one extra decision — **which provider section the id belongs to**:

| Audit id shape | Config section | Why |
|---|---|---|
| bare `anthropic.<model>` / `openai.<model>` | `bedrock-mantle` (In-Region, `/anthropic/v1/messages`, `/openai/v1/*`) | Mantle only accepts the bare id |
| `us.` / `eu.` / `au.` / `jp.` / `in.` geo profile | `bedrock` (bedrock-runtime) | runtime requires a profile id for these models; geo bills list +10% |
| `global.` profile | `bedrock`, priced **separately** | Global CRIS bills the plain list rate (~10% below geo); on a flat entry use `overrides: {"global.…": {input, output}}`, on a tiered entry make it its own model entry — `overrides` only works with flat pricing |

Never alias a `global.` id onto a geo-priced entry, and never put runtime profile ids under `bedrock-mantle`.

### Step 4a. Re-verify every row you are about to change (mandatory)

The audit is research output and has been wrong before (a promo encoded as list price; a long-context tier dropped because the pricing page showed "–" while the model card spelled it out; Bedrock profile ids filed under the wrong endpoint). Before editing `configs/base.yml`, open the audit row's `source` URL (and, for Bedrock, the AWS model card) for **every** row in the changeset that is not `no-op`, and confirm:

1. the number(s) you are about to write appear verbatim on that page (per-1M list rate, not batch / flex / cached / promo);
2. the tier shape: if the page or card says anything like *"prompts with >272K input tokens are priced at 2x input and 1.5x output"* and the context window exceeds that threshold, the entry is tiered even when the audit row has one tier — fix the audit row too;
3. for Bedrock, which endpoint/profile the id is documented for and whether the card prints different In-Region / Geo / Global prices;
4. for a `deprecated` / `shutdown` change, the sunset date and replacement on the deprecations page.

Then prove the resolved prices with a throwaway test in `internal/config/` (delete it afterwards) that calls `GetModelPricing(provider, model, tokens)` for each changed id and alias at 1k and 300k input tokens, and paste the output into the PR's Verification section. Anything that fails re-verification becomes an open question, not a config edit.

### Step 4b. Endpoint coverage

The proxy only bills paths that `ClassifyEndpoint` in `internal/providers/endpoints.go` returns as `EndpointMetered`; known non-billable paths are `EndpointPassthrough`; anything else is `EndpointUnknown` and is forwarded unbilled. Three sources feed this step:

1. **Vendor API reference (`providers.<p>.endpoints` in the audit).** For every `billable: yes` or `async` endpoint, check whether its path suffix is in `meteredSuffixes`. A billable endpoint missing from the list is a `new-endpoint` finding. A `billable: no` endpoint missing from `passthroughSuffixes`/`passthroughSegments` is a `new-passthrough` finding.
2. **Live proxy counters.** `GET https://llm.instawork.com/admin/api/model-status` returns `unmetered_total` and `by_unmetered` (`provider:/path/template` → calls today, with daily history when Redis is on). The same data is on the admin Model Status page under "Unmetered endpoint calls". Anything non-zero here is a finding regardless of what the docs say. The endpoint sits behind admin auth and answers **401** without a session cookie or admin API key; the vendor-audit automation has neither, so in an unattended run record "live counters: unreachable (401, no admin credential in the environment)" and rely on source 3 — do not spend more than one request confirming it.
3. **Datadog ELB logs (14 days).** Run the bundled CLI, which issues the logs-analytics aggregate and classifies every provider path with the same `ClassifyEndpoint` / `EndpointTemplate` the proxy bills from:

   ```bash
   make endpoint-coverage            # needs DD_API_KEY + DD_APPLICATION_KEY in the env
   go run ./cmd/endpoint-coverage/ -min-calls 1 -format table   # show one-off buckets too
   go run ./cmd/endpoint-coverage/ -input paths.tsv             # offline: "<count>\t<path>" lines
   ```

   Paste the markdown table into the PR body. `unknown` rows are the findings; `/bedrock/`, `/bedrock-mantle/` and bare `/anthropic` are scanner probes hitting the route root, not vendor endpoints. If you must call Datadog by hand, the exact request is in `cmd/endpoint-coverage/main.go` (`fetchDatadog`): `POST /api/v2/logs/analytics/aggregate`, `compute: [{aggregation: count, type: total}]`, `group_by` on `@http.url_details.path` with `limit: 1000` and `sort: {type: measure, aggregation: count, order: desc}`, and exclude `/health`, `/admin*`, `/` and `/lander` in the query — without the exclusion the bucket cap is spent on health checks. The proxy also logs `endpoint_class` / `endpoint_template` on every "Provider route tracked" / "Provider route not tracked" line, so `service:llm-proxy @endpoint_class:unknown` grouped by `@endpoint_template` is an equivalent app-log view once those logs are indexed.

For each `new-endpoint`, the fix is code, not config: add the suffix to `meteredSuffixes`, add a `looksLike…`/`parse…` branch to that provider's `ParseResponseMetadata` (and `IsStreamingRequest` if the endpoint streams), and add a fixture test plus a row in `TestTokenParsingMiddleware_EndpointCoverage`. Follow the Gemini Interactions branch in `internal/providers/gemini_interactions.go` as the template. Do not mark a billable endpoint passthrough to silence the counter.

### Step 5. Deprecation output

In addition to the pricing diff, emit a machine-readable deprecations list the user can copy elsewhere (client UI warnings, release notes, etc.). It is a straight projection of the audit — the first `jq` in step 2 — rendered as YAML and grouped by provider:

```yaml
deprecations:
  openai:
    - model: gpt-5
      replaced_by: gpt-5.6
      sunset_date: "2026-10-23"  # null when the vendor delisted without a date
      status: deprecated  # deprecated | shutdown | legacy
      source: https://developers.openai.com/api/docs/deprecations
      notes: Scheduled shutdown; entry marked deprecated with replacement
  anthropic: []
  gemini:
    - model: text-embedding-004
      replaced_by: gemini-embedding-001
      sunset_date: "2026-01-14"
      status: shutdown
      source: https://ai.google.dev/gemini-api/docs/deprecations
      notes: Moved to retired_models
  bedrock: []
```

Include this block in the plan/PR body. Do not write it to a separate file — the audit in `audits/` is already the durable record.

### Step 6. Present or apply

- In Plan mode: call `CreatePlan` with the diff + deprecations block. Surface open questions (e.g., tiered pricing, same-price aliases vs separate entries).
- In Agent mode: edit `configs/base.yml` directly, then go to step 7.

### Step 7. Verify after editing

Always run both commands before committing:

```bash
# 1. Semantic config validation (pricing sanity, duplicate aliases, structural correctness)
go run ./cmd/config-validator/

# 2. Unit tests (config parsing, tiered pricing logic, alias resolution)
go test ./internal/config/...
```

Also grep for any hardcoded price assertions in tests that touch changed models:

```bash
rg -n "\"<changed-model-id>\"" internal/ configs/
```

Update test expectations if tests assert specific prices that changed. The validator and test suite must both be clean before the PR is considered ready.

## Alias rules (non-negotiable)

The governing principle: **same version number = aliasable, different version number = never aliasable**.

An alias is acceptable when any of the following is true:
- It is a dated snapshot of the same model (e.g., `claude-opus-4-1-20250805`)
- It is a "latest" pointer for the same model (e.g., `claude-3-5-sonnet-latest`)
- It is a punctuation variant of the same model (e.g., `claude-opus-4.1` ↔ `claude-opus-4-1`, `gemini-3.1-pro` ↔ `gemini-3-1-pro`)
- It is a preview / experimental SKU for the same model version (e.g., `gemini-2.5-pro-preview` under `gemini-2.5-pro`, `gpt-4.1-2025-04-14-preview` under `gpt-4.1`). Preview aliases are fine as long as the version number matches.

Examples of **valid** aliases:
- `claude-opus-4-1` aliases `claude-opus-4-1-20250805` (dated snapshot)
- `gpt-4o` aliases `gpt-4o-2024-11-20` (latest snapshot pointer)
- `gemini-3.1-pro` aliases `gemini-3.1-pro-preview` and `gemini-3-1-pro` (preview SKU + punctuation variant, same version)
- `claude-sonnet-4-5` aliases `claude-sonnet-4.5` and `claude-sonnet-4-5-20250929`

Examples that are **NEVER** aliases (always separate entries), because the version number differs:
- `gpt-5` and `gpt-5.1` — different versions
- `gpt-5` and `gpt-5.2` — different versions, even if priced identically
- `claude-opus-4-0` and `claude-opus-4-1` — different versions
- `claude-opus-4-5` and `claude-opus-4-6` and `claude-opus-4-7` — each is its own entry
- `gemini-2.5-pro` and `gemini-3-pro-preview` — different version families
- `gemini-3-pro-preview` and `gemini-3.1-pro-preview` — 3 and 3.1 are different versions
- `o1` and `o1-pro` — different products
- Any model and its `-mini`/`-nano`/`-lite`/`-pro` sibling

When in doubt, create a separate entry. Over-aliasing silently routes traffic to the wrong model's pricing and rate limits, and masks deprecations.

Existing aliases are append-only (hard rule 8). A vendor de-listing a snapshot ID is not a reason to drop it from `aliases:`; an alias present in the config but missing from the audit's `aliases` array is informational, not an action item.

## Snippet templates

Flat pricing (most common):

```yaml
"<model-id>":
  enabled: true
  aliases: ["<dated-snapshot>", "<vendor-documented-same-model-alias>"]
  limits:
    # copy from sibling model of same tier
    tokens_per_minute: 80_000
    requests_per_minute: 1_000
    tokens_per_day: 1_920_000
    requests_per_day: 1_440_000
    burst_tokens: 8_000
    burst_requests: 100
  pricing:
    input: <in>
    output: <out>
```

Tiered pricing (Gemini Pro, Anthropic >200k):

```yaml
"<model-id>":
  enabled: true
  aliases: []
  limits: { ... }
  pricing:
    - threshold: 200_000
      input: <low-tier-in>
      output: <low-tier-out>
    - threshold: 0  # fallback for prompts above threshold
      input: <high-tier-in>
      output: <high-tier-out>
```

Per-snapshot override (snapshot priced differently from the alias base):

```yaml
"gpt-4o":
  pricing:
    input: 2.50
    output: 10.00
    overrides:
      "gpt-4o-2024-05-13":
        input: 5.00
        output: 15.00
```

## Output format

At the end of the run, produce three artifacts, and name the audit file they came from (`audits/audit-MM-DD-YYYY.json`, `generated_at`) at the top:

1. **Pricing changeset** — a list of edits grouped by provider, with old vs new prices. Flag each entry as `fix-stale`, `price-change`, `new-model`, `deprecated`, `remove-shutdown`, or `no-op`.
2. **Deprecations list** — the YAML block above, projected from the audit. Always include it, even if empty (emit `deprecations: {openai: [], anthropic: [], gemini: [], bedrock: []}`). Entries with `status: shutdown` SHOULD correspond to `remove-shutdown` entries in the changeset.
3. **Endpoint coverage** — one table with a row per finding from Step 4b: provider, method + path, class today (`metered` / `passthrough` / `unknown`), evidence (vendor docs / `by_unmetered` count / Datadog 14-day count), and action (`new-endpoint` → needs a parser, `new-passthrough` → add to registry, `ok`). Always include it; if nothing is unmetered, say so explicitly with the counters that prove it.
