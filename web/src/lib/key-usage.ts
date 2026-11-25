import type { APIKey, KeyStatsResponse } from "../types";

/**
 * Whether a key has any recorded traffic. The persisted first-request marker wins; otherwise
 * fall back to live stats so keys used before the marker existed still count as used.
 */
export function keyHasBeenUsed(
  keyRecord: Pick<APIKey, "first_request_at"> | undefined,
  stats: Partial<Pick<KeyStatsResponse, "cost_today" | "cost_month" | "unmetered_today" | "recent_cost">> | undefined,
): boolean {
  if (keyRecord?.first_request_at) return true;
  if (!stats) return false;
  return (
    (stats.cost_today?.requests ?? 0) > 0
    || (stats.unmetered_today?.requests ?? 0) > 0
    || (stats.cost_month?.spend_usd ?? 0) > 0
    || (stats.recent_cost?.length ?? 0) > 0
  );
}
