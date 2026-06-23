*AWHR: AI Written, Human Reviewed by me*

# PII / PHI Egress Remediation Plan

Derived from the 2026-06-11 PII egress audit of `llm-proxy-admin-dashboard`.
This is a remediation design, grouped into independently-shippable workstreams
ordered by severity. Each item lists the audit finding ID, the concrete change,
and the file(s) involved.

> Status legend: ☐ not started · ◐ in progress · ☑ done

---

## Decision gate 0 — Vendor BAA / DPA posture (blocks W1 severity)

The three LLM-path P0s only matter if vendors lack a contractual safeguard.
Resolve this first; it sets whether W1 is "P0, must fix before GA" or
"acceptable, document it."

- ☐ Confirm BAA/DPA status for **OpenAI, Anthropic, Google Gemini**.
- ☐ Confirm whether **AWS Bedrock** is invoked in an account under an AWS BAA.
- ☐ Record the answers in `docs/PII_REDACT.md` as the canonical posture.

**Outcome:** if a vendor is covered, its raw-egress findings drop from P0 to
informational. If not, W1 is required before that vendor is enabled in prod.

---

## W1 — Make redaction the safe default on the egress path (P0: LLM-01/02/03)

Goal: when redaction is enabled, raw prompt/response bytes never reach a
non-BAA vendor through any path. Today three escape hatches defeat this.

- ☐ **LLM-01 — default-on.** Flip `features.pii_redact.enabled` to `true` in
  `configs/base.yml` (or make prod/sidecar configs set it explicitly), so the
  redaction middleware is always installed unless deliberately disabled.
  - Files: `configs/base.yml:33`, `configs/production.yml`, `cmd/llm-proxy/main.go:~1187`.
- ☐ **LLM-03 — remove the wire-mode raw passthrough.** Treat
  `wire_placeholders:false` (log-only scrub, raw body still proxied) as a
  removed/disallowed mode, or gate it behind an explicit
  `allow_unsafe_observability_mode` flag that fails config validation in prod.
  - Files: `internal/middleware/pii_redact.go:128-131,214-219,251-261`, `internal/config/config.go:124-126`.
- ☐ **LLM-04 — fail closed by default.** Change the default `fail_mode` to
  `"closed"` (reject on sidecar error) for prod, or make `fail_mode:"open"`
  require an explicit opt-in. A flaky Presidio sidecar must not silently leak.
  - Files: `internal/middleware/pii_redact.go:236-244`, `configs/base.yml:40`.
- ☐ **LLM-05 — oversize handling.** For bodies over `max_body_bytes`, fail
  closed (reject) rather than forwarding raw, or chunk+scrub. At minimum emit a
  counter so oversize bypasses are observable.
  - Files: `internal/middleware/pii_redact.go:152-159,188-202`.
- ☐ **LLM-02 / LLM-06 — per-key off + admin bypass.** Keep the Bedrock-only
  guard for `redact_pii:false` keys. The bypass allowlist is now config-driven
  (`features.admin_dashboard.pii_off_bypass_admins`); audit who is on it and
  ensure request-time `EnforcePIIOffBedrockProvider` has no gaps.
  - Files: `internal/apikeys/pii_bedrock_policy.go`, `internal/middleware/apikey_validation.go:57`.

**Exit criteria:** an integration test proving that with redaction enabled, a
known PII payload reaches the upstream stub as `[REDACTED:*]` across all of:
default path, sidecar-error path, oversize path, and `wire_placeholders` path.

---

## W4 — Session / key hardening (P3: API-01, AUTH-01/02)

- ☐ **AUTH-01 — cookie.** Add an encryption (block) key to the
  `gorilla/sessions.NewCookieStore` so the admin identity payload is encrypted,
  not just signed; default `Secure=true`; define a rotation policy.
  - Files: `internal/admin/auth.go:67-74,313-315`.
- ☐ **API-01 — share endpoint.** Already OAuth-gated with a 24h TTL and the
  upstream provider secret masked; add an audit log on share access/creation.
  - Files: `internal/admin/handlers.go:472-515`, `internal/apikeys/share.go`.
- ☐ **Open question — provider key at rest.** Verify whether the real upstream
  provider secret stored in DynamoDB (`internal/apikeys/store.go` CreateKey/
  marshal path) is KMS-wrapped at rest, not just masked in API responses.

---

## Suggested sequencing

1. **Decision gate 0** (unblocks everything; no code).
2. **W1** — egress redaction default-on, fail-closed, escape-hatch removal.
3. **W4** — session/key hardening, can land anytime.
