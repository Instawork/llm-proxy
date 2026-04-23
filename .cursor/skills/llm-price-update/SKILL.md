---
name: llm-price-update
description: Refresh LLM provider pricing (OpenAI, Anthropic, Google Gemini) in configs/base.yml for the llm-proxy app. Use when the user asks to "update llm prices", "refresh model pricing", "sync llm-proxy pricing", "check llm pricing", "llm price update", or otherwise wants the llm-proxy pricing config audited against current vendor pricing.
---

# llm-price-update

Audit and update model pricing in [configs/base.yml](../../../configs/base.yml) for the `llm-proxy` app against the current published prices from OpenAI, Anthropic, and Google. Produce both the pricing diff and a deprecation list.

## Hard rules

1. **Always use the live web.** Do NOT rely on model knowledge for prices or model lineups. Prices change frequently; the agent's training data is stale.
2. **Always dispatch parallel subagents** (one per provider) to gather pricing. Then verify the most surprising claims with direct `WebSearch` calls before writing the config. Subagents can hallucinate; independent verification is required for any new model family, price drop >20%, or price increase.
3. **Never add aliases for models that are actually different versions.** Aliases are only for the same underlying model (e.g., `claude-opus-4-1` ↔ `claude-opus-4-1-20250805`, `gpt-4o` ↔ `gpt-4o-2024-11-20`). Never alias `gpt-5` to `gpt-5.1`, `claude-opus-4-0` to `claude-opus-4-1`, `gemini-2.5-pro` to `gemini-3-pro`, etc. Different version numbers = different models = separate entries.
4. **Always encode tiered pricing when the vendor publishes it.** This is not optional. If the vendor lists different input/output rates based on prompt length (e.g., Gemini Pro ≤200k vs >200k, Gemini 1.5 ≤128k vs >128k, Anthropic Sonnet 4.x ≤200k vs >200k), the model MUST be configured as a `PricingTier` list — never collapsed to flat `{input, output}`. Flat pricing silently under-bills long-prompt traffic.
5. **Always produce a deprecations list** alongside the pricing diff so it can be consumed by downstream callers (e.g., client UI, docs). See "Deprecation output" below.

## Workflow

Copy this checklist at the start of a run:

```
Task progress:
- [ ] 1. Read configs/base.yml to capture current pricing and models
- [ ] 2. Dispatch 3 parallel subagents (OpenAI, Anthropic, Google) to fetch current prices
- [ ] 3. Verify surprising findings with direct WebSearch
- [ ] 4. Diff vendor data against configs/base.yml
- [ ] 5. Build pricing changeset + deprecations list
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

When building the diff, explicitly check each new model's vendor pricing page for "long context", ">200k", ">128k", "prompts above X tokens", or a second pricing row. If any exists, encode it as tiers.

### Step 2. Dispatch parallel subagents

Launch all three in one tool-call batch using `subagent_type: generalPurpose`. Each gets the current list of models in its provider from `configs/base.yml`, and is told to return per-million-token standard-tier prices (non-cached, non-batch) plus any new models released since the config was last updated.

Prompt skeleton (adapt per provider):

> Go to `<vendor pricing URL>` and find the CURRENT pricing as of `<today>` for all of the following models. Return per-million-token input and output prices in USD for the standard (non-cached, non-batch) tier. Cross-reference against a second source (OpenRouter, artificialanalysis, vendor docs) if possible.
>
> Models needed: `<list from base.yml>`
>
> Also list ANY NEW `<vendor>` models released since `<assumed cutoff>` that we should be aware of. For each new model, confirm:
> - Official API model name (NOT aliases)
> - Standard input/output $/1M
> - Whether it has tiered pricing (e.g., >200k input tokens)
> - Release date
> - Deprecation status and sunset date of any old model it replaces
>
> Format response as a compact table with columns: model, input $/1M, output $/1M, tier_threshold, release_date, deprecated_by, source URL, notes.

Vendor URLs (primary / secondary):
- OpenAI: `https://platform.openai.com/docs/pricing` / `https://openai.com/api/pricing/`
- Anthropic: `https://docs.anthropic.com/en/docs/about-claude/pricing` / `https://www.anthropic.com/pricing`
- Google: `https://ai.google.dev/gemini-api/docs/pricing` / `https://cloud.google.com/vertex-ai/generative-ai/pricing`

### Step 3. Verify surprising findings

For any subagent claim that looks suspicious — new major version (e.g., "GPT-6"), price change >20%, model name you don't recognize — run a direct `WebSearch` with 2-3 queries to confirm. If verification fails, drop the claim and note it in the plan.

### Step 4. Build the diff

For each provider section of `configs/base.yml`, categorize every model:

| Category | Action |
|---|---|
| Unchanged | Skip |
| Price changed | Update `input`/`output`; if an alias has a different price, move it into `overrides` |
| New model | Add entry with canonical ID + same-model aliases only (see rule 3) |
| Deprecated/sunset | Keep in config (callers still reference it), but add to deprecations list |
| Removed from vendor | Only remove if vendor returns 404 for API calls; otherwise keep |

Reuse the `limits` block of a similar existing model rather than inventing new rate-limit numbers — e.g., a new flagship uses the same limits as the previous flagship.

### Step 5. Deprecation output

In addition to the pricing diff, emit a machine-readable deprecations list the user can copy elsewhere (client UI warnings, release notes, etc.). Format:

```yaml
deprecations:
  openai:
    - model: o1-preview
      replaced_by: o1
      sunset_date: null  # or ISO date if vendor announced one
      source: <URL>
      notes: Removed from platform pricing page
  anthropic:
    - model: claude-3-5-sonnet
      replaced_by: claude-sonnet-4-5
      sunset_date: null
      source: <URL>
      notes: Dropped from marketing page; still listed on docs pricing
  gemini:
    - model: gemini-3-pro-preview
      replaced_by: gemini-3.1-pro
      sunset_date: null
      source: <URL>
      notes: Marked deprecated on AI Studio models page
```

Include this block in the plan/PR body and, if the user asks, write it to a file (suggest `docs/model-deprecations.yml` or similar — do not create it unprompted).

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

At the end of the run, produce two artifacts:

1. **Pricing changeset** — a list of edits grouped by provider, with old vs new prices. Flag each entry as `fix-stale`, `price-change`, `new-model`, or `no-op`.
2. **Deprecations list** — the YAML block above. Always include it, even if empty (emit `deprecations: {openai: [], anthropic: [], gemini: []}`).
