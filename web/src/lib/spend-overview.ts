import type { SpendCaveat, SpendKeyRow } from "../types";

/** Above this many keys the overview switches from cards to a table. */
export const SPEND_KEY_CARD_LIMIT = 6;

export const CAVEAT_COPY: Record<SpendCaveat, string> = {
  rollup_off: "Redis rollups are unavailable — figures reflect this proxy instance only and reset on restart.",
  history_top_n: "Prior days are read from daily archives that keep only the top 100 keys and users, so small spenders may be under-counted before today.",
  provider_month_partial: "Provider month-to-date only covers the retained daily archives, so earlier days in this month are missing.",
};

/** Spend measured against the key's cap, and how far along it is (null when uncapped). */
export function capFraction(row: Pick<SpendKeyRow, "cap" | "today" | "month">): {
  spentUsd: number;
  limitCents: number;
  fraction: number;
  label: string;
} | null {
  if (!row.cap || row.cap.cents <= 0) return null;
  const spentUsd = row.cap.period === "monthly" ? row.month.spend_usd : row.today.spend_usd;
  const limitUsd = row.cap.cents / 100;
  return {
    spentUsd,
    limitCents: row.cap.cents,
    fraction: spentUsd / limitUsd,
    label: row.cap.period === "monthly" ? "Monthly limit" : "Daily limit",
  };
}

/** Month spend descending, then today, then a stable id tiebreak. */
export function sortBySpend<T extends { today: { spend_usd: number }; month: { spend_usd: number } }>(
  rows: T[],
  id: (row: T) => string,
): T[] {
  return [...rows].sort((a, b) => {
    if (b.month.spend_usd !== a.month.spend_usd) return b.month.spend_usd - a.month.spend_usd;
    if (b.today.spend_usd !== a.today.spend_usd) return b.today.spend_usd - a.today.spend_usd;
    return id(a).localeCompare(id(b));
  });
}
