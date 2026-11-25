import type { SpendCaveat, SpendKeyRow, UnmeteredStats } from "../types";

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

export const UNMETERED_HELP =
  "Requests that reached the provider but returned no token usage, so nothing was billed to the key — embeddings, model listings, and calls that failed (shown with their HTTP status).";

/** Short endpoint list for compact rows: "/v1/embeddings, /v1/models +2 more". */
export function unmeteredEndpointSummary(stats: Pick<UnmeteredStats, "endpoints">, max = 2): string {
  const names = stats.endpoints.map((e) => e.endpoint);
  const shown = names.slice(0, max).join(", ");
  const rest = names.length - max;
  return rest > 0 ? `${shown} +${rest} more` : shown;
}

export type SpendView = "all" | "mine" | "shared";

export const DEFAULT_SPEND_VIEW: SpendView = "mine";

export const SPEND_VIEW_OPTIONS: { value: SpendView; label: string; hint: string }[] = [
  { value: "all", label: "All spend", hint: "Keys you own and keys shared with you" },
  { value: "mine", label: "My spend", hint: "Keys you own" },
  { value: "shared", label: "Shared key spend", hint: "Keys shared with you (read-only)" },
];

/** Reads the `scope` URL value; anything unknown falls back to the default view. */
export function parseSpendView(raw: string | null | undefined): SpendView {
  return SPEND_VIEW_OPTIONS.find((o) => o.value === raw)?.value ?? DEFAULT_SPEND_VIEW;
}

/** Whether a key row counts toward the totals of the selected view. */
export function rowInSpendTotal(row: Pick<SpendKeyRow, "shared">, view: SpendView): boolean {
  if (view === "all") return true;
  return view === "shared" ? Boolean(row.shared) : !row.shared;
}

export interface OverviewScopeCopy {
  /** Heading for the spend totals panel. */
  totalsTitle: string;
  /** Short scope phrase reused in subtitles ("keys you own"); empty for the fleet view. */
  scopePhrase: string;
  /** Visible exclusion note, or null when nothing is excluded. */
  excludesNote: string | null;
  /** Note on a key row that does not count toward the totals, or null when it does. */
  rowNote: (row: Pick<SpendKeyRow, "shared">) => string | null;
}

function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

/**
 * One source for the wording that tells a viewer what the overview totals, provider breakdown,
 * daily chart and unmetered count cover, so every section uses the same phrasing. `view` is null
 * for the fleet (editor/admin) overview, which has no scoping.
 */
export function overviewScopeCopy(
  view: SpendView | null,
  counts: { owned: number; shared: number },
): OverviewScopeCopy {
  if (view === null) {
    return { totalsTitle: "Spend", scopePhrase: "", excludesNote: null, rowNote: () => null };
  }
  const rowNote = (row: Pick<SpendKeyRow, "shared">) =>
    rowInSpendTotal(row, view)
      ? null
      : view === "mine"
        ? "Not included in your spend total"
        : "Not included in the shared key spend total";

  if (view === "all") {
    return {
      totalsTitle: "All spend (keys you own + keys shared with you)",
      scopePhrase: "all keys you can see",
      excludesNote: null,
      rowNote,
    };
  }
  if (view === "shared") {
    return {
      totalsTitle: "Shared key spend (keys shared with you)",
      scopePhrase: "keys shared with you",
      excludesNote:
        counts.owned > 0
          ? `Excludes your ${plural(counts.owned, "own key")} — see each key for its spend.`
          : null,
      rowNote,
    };
  }
  return {
    totalsTitle: "Your spend (keys you own)",
    scopePhrase: "keys you own",
    excludesNote:
      counts.shared > 0
        ? `Excludes ${plural(counts.shared, "shared key")} — see each key for its spend.`
        : null,
    rowNote,
  };
}
