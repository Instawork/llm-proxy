import { maskKeyId } from "./format";
import { redactedRateLimitScopeForKey } from "./key-routes";
import type {
  CostRecentEvent,
  CostStats,
  PIIRecentEvent,
  PIIStats,
  RateLimitConfig,
  RateLimitsResponse,
} from "../types";

export function costStatsForKey(stats: CostStats | undefined, key: string) {
  const masked = maskKeyId(key);
  return stats?.by_key?.find((row) => row.key_id === masked || row.key_id === key);
}

export function costRecentForKey(stats: CostStats | undefined, key: string): CostRecentEvent[] {
  const masked = maskKeyId(key);
  return (stats?.recent ?? []).filter((ev) => ev.key_id === masked || ev.key_id === key);
}

export function piiRecentForKey(stats: PIIStats | undefined, key: string): PIIRecentEvent[] {
  const masked = maskKeyId(key);
  return (stats?.recent ?? []).filter((ev) => ev.key_id === masked);
}

export function piiDetectionsForKey(stats: PIIStats | undefined, key: string): number {
  const masked = maskKeyId(key);
  const fromTop = stats?.top_keys?.find((row) => row.name === masked)?.count;
  if (fromTop !== undefined) return fromTop;
  return piiRecentForKey(stats, key).filter((ev) => ev.entity_total > 0).length;
}

export function rateLimitScopeForKey(key: string): string {
  return `key:${key}`;
}

function counterForKeyScope(
  data: RateLimitsResponse | undefined,
  window: "day" | "minute",
  key: string,
) {
  const counters = data?.snapshot?.[window]?.counters;
  if (!counters) return undefined;
  return counters[rateLimitScopeForKey(key)] ?? counters[redactedRateLimitScopeForKey(key)];
}

export function rateLimitUsageForKey(
  data: RateLimitsResponse | undefined,
  key: string,
): { window: "day" | "minute"; requests: number; tokens: number }[] {
  return (["day", "minute"] as const)
    .map((window) => {
      const counter = counterForKeyScope(data, window, key);
      return {
        window,
        requests: counter?.requests ?? 0,
        tokens: counter?.tokens ?? 0,
      };
    })
    .filter((row) => row.requests > 0 || row.tokens > 0);
}

export function rateLimitOverrideForKey(
  data: RateLimitsResponse | undefined,
  key: string,
): RateLimitConfig | undefined {
  const perKey = data?.overrides?.PerKey;
  if (!perKey) return undefined;
  return perKey[key] ?? perKey[redactedRateLimitScopeForKey(key)];
}
