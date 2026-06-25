import type { APIKey } from "../types";
import { trimProxyKeyPrefix } from "./proxy-key";

const compactFormatter = new Intl.NumberFormat("en-US", {
  notation: "compact",
  maximumFractionDigits: 1,
});

/** Formats large counts compactly: 1234 -> "1.2K", 1_500_000 -> "1.5M". */
export function compact(n: number | undefined | null): string {
  if (n === undefined || n === null || Number.isNaN(n)) return "—";
  if (Math.abs(n) < 1000) return String(n);
  return compactFormatter.format(n);
}

/** Locale-formats an integer count, tolerating undefined/null/NaN from the API. */
export function formatCount(n: number | undefined | null): string {
  if (n === undefined || n === null || Number.isNaN(n)) return "0";
  return n.toLocaleString();
}

/** Formats a 0..1 ratio as a percentage string, e.g. 0.667 -> "66.7%". */
export function percent(ratio: number | undefined | null, digits = 1): string {
  if (ratio === undefined || ratio === null || Number.isNaN(ratio)) return "—";
  return `${(ratio * 100).toFixed(digits)}%`;
}

export type CostLimitPeriod = "daily" | "monthly";

/** Formats per-key daily cost limit (cents). Zero means unlimited. */
export function formatDailyCostLimit(cents: number | undefined | null): string {
  if (cents === undefined || cents === null || cents <= 0) return "Unlimited";
  return `$${(cents / 100).toFixed(2)}/day`;
}

/** Formats per-key monthly cost limit (cents). Zero means unlimited. */
export function formatMonthlyCostLimit(cents: number | undefined | null): string {
  if (cents === undefined || cents === null || cents <= 0) return "Unlimited";
  return `$${(cents / 100).toFixed(2)}/month`;
}

/** Formats API month keys like `2026-06` for display. */
export function formatMonthYear(month: string | undefined | null): string {
  if (!month) return "This month";
  const [year, mon] = month.split("-").map(Number);
  if (!year || !mon) return "This month";
  return new Date(Date.UTC(year, mon - 1, 1)).toLocaleString("en-US", {
    month: "short",
    year: "numeric",
    timeZone: "UTC",
  });
}

export function isPersonalKey(
  key: Pick<APIKey, "tags" | "owner_email">,
): boolean {
  if (key.tags?.personal === "true") return true;
  return Boolean(key.owner_email?.trim());
}

/** Which spend cap window applies to this key. */
export function keyCostLimitPeriod(
  key: Pick<
    APIKey,
    "tags" | "owner_email" | "daily_cost_limit" | "monthly_cost_limit"
  >,
): CostLimitPeriod {
  if (isPersonalKey(key)) return "monthly";
  if (key.monthly_cost_limit && key.monthly_cost_limit > 0) return "monthly";
  return "daily";
}

/** Human-readable spend cap for the keys table. */
export function formatKeySpendCap(
  key: Pick<
    APIKey,
    "tags" | "owner_email" | "daily_cost_limit" | "monthly_cost_limit"
  >,
): string {
  if (keyCostLimitPeriod(key) === "monthly") {
    return formatMonthlyCostLimit(key.monthly_cost_limit);
  }
  return formatDailyCostLimit(key.daily_cost_limit);
}

/** Active spend cap in cents for the configured window. */
export function keySpendCapCents(
  key: Pick<
    APIKey,
    "tags" | "owner_email" | "daily_cost_limit" | "monthly_cost_limit"
  >,
): number {
  if (keyCostLimitPeriod(key) === "monthly") {
    return key.monthly_cost_limit ?? 0;
  }
  return key.daily_cost_limit ?? 0;
}

/** Effective monthly spend cap in cents (explicit key cap or personal default). */
export function effectiveMonthlyLimitCents(
  key: Pick<APIKey, "tags" | "owner_email" | "monthly_cost_limit">,
  viewerMonthlyCents = 0,
): number {
  if (key.monthly_cost_limit && key.monthly_cost_limit > 0) {
    return key.monthly_cost_limit;
  }
  if (isPersonalKey(key) && viewerMonthlyCents > 0) {
    return viewerMonthlyCents;
  }
  return 0;
}

/** Effective daily spend cap in cents (org keys with a daily cap only). */
export function effectiveDailyLimitCents(
  key: Pick<
    APIKey,
    "tags" | "owner_email" | "daily_cost_limit" | "monthly_cost_limit"
  >,
): number {
  if (isPersonalKey(key) || keyCostLimitPeriod(key) !== "daily") {
    return 0;
  }
  return key.daily_cost_limit ?? 0;
}

/** Dollars string for the key edit form. Empty when unlimited (0 cents). */
export function costLimitFormDollars(cents: number | undefined | null): string {
  if (cents === undefined || cents === null || cents <= 0) return "";
  return String(cents / 100);
}

/** @deprecated use costLimitFormDollars */
export function dailyCostLimitFormDollars(cents: number | undefined | null): string {
  return costLimitFormDollars(cents);
}

export function costLimitFormFromKey(
  key: Pick<
    APIKey,
    "tags" | "owner_email" | "daily_cost_limit" | "monthly_cost_limit"
  >,
): { period: CostLimitPeriod; dollars: string } {
  const period = keyCostLimitPeriod(key);
  const cents =
    period === "monthly" ? key.monthly_cost_limit ?? 0 : key.daily_cost_limit ?? 0;
  return { period, dollars: costLimitFormDollars(cents) };
}

/** Formats USD spend from the cost tracker (already in dollars, not cents). */
export function formatUsd(amount: number | undefined | null, digits = 4): string {
  if (amount === undefined || amount === null || Number.isNaN(amount)) return "—";
  if (amount === 0) return "$0.00";
  if (Math.abs(amount) < 0.01) return `$${amount.toFixed(digits)}`;
  return `$${amount.toFixed(2)}`;
}

const MASKED_CREDENTIAL_SPLIT = /…|\u2026|\.\.\./;

export const MASKED_CREDENTIAL_HASH_TITLE =
  "FNV-1a/32 hash of the full credential — not the secret suffix";

/** Parses `prefix…hash` masked credential ids (proxy keys and BYO credentials). */
export function parseMaskedCredentialId(
  maskedId: string,
): { prefix: string; hash: string } | null {
  const trimmed = maskedId.trim();
  if (!trimmed) return null;
  const match = trimmed.match(MASKED_CREDENTIAL_SPLIT);
  if (!match || match.index === undefined) return null;
  const hash = trimmed.slice(match.index + match[0].length);
  if (!/^[0-9a-f]{8}$/.test(hash)) return null;
  const prefix = trimmed.slice(0, match.index);
  if (!prefix) return null;
  return { prefix, hash };
}

/** Masked proxy key identity matching middleware.MaskKeyID for joining spend stats.
 * A bare 12-char prefix collides across keys sharing that prefix, so we append
 * an FNV-1a/32 hash of the whole key (mirrors the Go backend byte-for-byte;
 * keys are ASCII so char/byte encodings agree). */
export function maskKeyId(key: string): string {
  if (!key) return "";
  if (key.length <= 12) return key;
  return `${key.slice(0, 12)}…${fnv1a32Hex(key)}`;
}

export function fnv1a32Hex(s: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(16).padStart(8, "0");
}

const SCOPE_SUFFIX_LEN = 4;

function redactScopeSecret(value: string): string {
  const body = trimProxyKeyPrefix(value);
  if (body.length <= SCOPE_SUFFIX_LEN) return "••••";
  return `••••${body.slice(-SCOPE_SUFFIX_LEN)}`;
}

function redactUserIPScope(rest: string): string {
  const ipPart = rest.startsWith("ip:") ? rest.slice(3) : rest;
  const lastColon = ipPart.lastIndexOf(":");
  if (lastColon >= 0) {
    const port = ipPart.slice(lastColon + 1);
    return `•••.•••.•••.• ·${port}`;
  }
  return "••••";
}

/** Humanizes a scope key for charts/tables; redacts API keys and client IPs. */
export function scopeLabel(scope: string): string {
  const kind = scopeKind(scope);
  const idx = scope.indexOf(":");
  if (idx < 0) return scope;

  const rest = scope.slice(idx + 1);
  if (kind === "key") return `key ${redactScopeSecret(rest)}`;
  if (kind === "user") {
    if (rest.startsWith("ip:")) return `user ${redactUserIPScope(rest)}`;
    return `user ${redactScopeSecret(rest)}`;
  }
  return rest;
}

/** Returns the scope kind prefix ("model" | "provider" | "user" | "global"). */
export function scopeKind(scope: string): string {
  const idx = scope.indexOf(":");
  return idx >= 0 ? scope.slice(0, idx) : scope;
}

/** Whether a breaker key is still tripping traffic (vs historical blocks today). */
export function isBreakerKeyCurrentlyOpen(
  key: string,
  providers: Record<string, { state?: string }>,
  openKeys: ReadonlySet<string>,
): boolean {
  if (openKeys.has(key)) return true;

  const colonIdx = key.indexOf(":");
  if (colonIdx < 0) {
    const state = providers[key]?.state;
    return state === "open" || state === "half-open" || state === "half_open";
  }

  return false;
}

/** Splits a circuit breaker store key into provider scope vs per-model scope. */
export function parseBreakerKey(
  key: string | undefined,
  provider: string,
): { model: string | null; scope: "model" | "provider" } {
  const raw = (key ?? provider).trim();
  const idx = raw.indexOf(":");
  if (idx < 0) {
    return { model: null, scope: "provider" };
  }
  const model = raw.slice(idx + 1);
  if (!model) {
    return { model: null, scope: "provider" };
  }
  return { model, scope: "model" };
}

/** Short relative time: "just now", "12s ago", "3m ago". */
export function relativeTime(from: number, now: number = Date.now()): string {
  const secs = Math.max(0, Math.round((now - from) / 1000));
  if (secs < 3) return "just now";
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.round(mins / 60);
  return `${hrs}h ago`;
}
