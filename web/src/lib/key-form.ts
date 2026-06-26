import type { CostLimitPeriod } from "./format";
import { costLimitFormFromKey } from "./format";
import type { APIKey, PiiRedactSetting, Provider } from "../types";

export const KEY_PROVIDERS: Provider[] = ["openai", "anthropic", "gemini", "bedrock"];
export const VIEWER_PROVIDERS: Provider[] = ["openai", "anthropic", "gemini"];

export type PiiFormValue = "inherit" | "on" | "off";

export type KeyFormState = {
  provider: Provider;
  actual_key: string;
  description: string;
  cost_limit_period: CostLimitPeriod;
  cost_limit_dollars: string;
  enabled: boolean;
  redact_pii: PiiFormValue;
  rate_limit_rpm: string;
  rate_limit_tpm: string;
  rate_limit_rpd: string;
  rate_limit_tpd: string;
  anthropic_tier: string;
};

export const defaultKeyForm: KeyFormState = {
  provider: "openai",
  actual_key: "",
  description: "",
  cost_limit_period: "daily",
  cost_limit_dollars: "100",
  enabled: true,
  redact_pii: "inherit",
  rate_limit_rpm: "",
  rate_limit_tpm: "",
  rate_limit_rpd: "",
  rate_limit_tpd: "",
  anthropic_tier: "metered",
};

export function piiToFormValue(value?: PiiRedactSetting): PiiFormValue {
  if (value === true) return "on";
  if (value === false) return "off";
  return "inherit";
}

export function piiFromFormValue(value: PiiFormValue): PiiRedactSetting {
  if (value === "on") return true;
  if (value === "off") return false;
  return null;
}

export function piiLabel(value?: PiiRedactSetting): string {
  if (value === true) return "On";
  if (value === false) return "Off";
  return "Inherit";
}

export function formPiiOffRequiresBedrock(
  redactPii: PiiFormValue,
  globalPiiEnabled: boolean,
  canBypass: boolean,
): boolean {
  if (canBypass) return false;
  if (redactPii === "off") return true;
  if (redactPii === "inherit" && globalPiiEnabled) return false;
  return false;
}

export function parseLimitField(value: string): number {
  const n = Number(value.trim());
  return Number.isFinite(n) && n > 0 ? Math.round(n) : 0;
}

export function rateLimitsFromForm(form: KeyFormState) {
  return {
    rate_limit_rpm: parseLimitField(form.rate_limit_rpm),
    rate_limit_tpm: parseLimitField(form.rate_limit_tpm),
    rate_limit_rpd: parseLimitField(form.rate_limit_rpd),
    rate_limit_tpd: parseLimitField(form.rate_limit_tpd),
  };
}

export function costLimitsFromForm(form: KeyFormState): {
  daily_cost_limit: number;
  monthly_cost_limit: number;
} {
  const cents = Math.round(Number(form.cost_limit_dollars || "0") * 100);
  if (form.cost_limit_period === "monthly") {
    return { daily_cost_limit: 0, monthly_cost_limit: cents };
  }
  return { daily_cost_limit: cents, monthly_cost_limit: 0 };
}

export function keyFormFromRecord(
  record: APIKey,
  anthropicDefaultTier: string,
): KeyFormState {
  const costLimit = costLimitFormFromKey(record);
  return {
    provider: record.provider,
    actual_key: "",
    description: record.description ?? "",
    cost_limit_period: costLimit.period,
    cost_limit_dollars: costLimit.dollars,
    enabled: record.enabled,
    redact_pii: piiToFormValue(record.redact_pii),
    rate_limit_rpm: record.rate_limit_rpm ? String(record.rate_limit_rpm) : "",
    rate_limit_tpm: record.rate_limit_tpm ? String(record.rate_limit_tpm) : "",
    rate_limit_rpd: record.rate_limit_rpd ? String(record.rate_limit_rpd) : "",
    rate_limit_tpd: record.rate_limit_tpd ? String(record.rate_limit_tpd) : "",
    anthropic_tier: record.tags?.tier ?? anthropicDefaultTier,
  };
}

export function formatRateLimits(record: APIKey): string {
  const parts: string[] = [];
  if (record.rate_limit_rpm) parts.push(`${record.rate_limit_rpm} rpm`);
  if (record.rate_limit_tpm) parts.push(`${record.rate_limit_tpm.toLocaleString()} tpm`);
  if (record.rate_limit_rpd) parts.push(`${record.rate_limit_rpd} rpd`);
  if (record.rate_limit_tpd) parts.push(`${record.rate_limit_tpd.toLocaleString()} tpd`);
  return parts.length ? parts.join(" · ") : "—";
}

export type KeyFormTab = "general" | "cost" | "pii" | "rate-limits";

export function modalTabClass(active: boolean): string {
  return active
    ? "btn btn-primary btn-sm gap-2 shadow-sm"
    : "btn btn-ghost btn-sm gap-2 text-base-content/70 hover:bg-base-200/70 hover:text-base-content";
}
