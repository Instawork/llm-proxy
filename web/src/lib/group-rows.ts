import type { CostKeyAgg, CostUserAgg, ProviderSpendAgg } from "./daily-history";
import type { NameCount } from "./daily-history";

export const DEFAULT_TOP_N = 10;

export function othersLabel(count: number): string {
  return `(and ${count} other${count === 1 ? "" : "s"})`;
}

export function splitTopByMetric<T>(rows: T[], getMetric: (row: T) => number, topN = DEFAULT_TOP_N) {
  const sorted = [...rows].sort((a, b) => getMetric(b) - getMetric(a));
  return {
    top: sorted.slice(0, topN),
    rest: sorted.slice(topN),
  };
}

/** @deprecated use splitTopByMetric */
export const splitTopBySpend = splitTopByMetric;

export function topDisplayRows<T>(
  rows: T[],
  getMetric: (row: T) => number,
  aggregate: (rest: T[]) => T,
  label: (aggregated: T, count: number) => T,
  topN = DEFAULT_TOP_N,
  collapse = true,
): (T & { isOthers?: boolean })[] {
  if (!collapse || rows.length <= topN) {
    return [...rows].sort((a, b) => getMetric(b) - getMetric(a)) as (T & { isOthers?: boolean })[];
  }

  const { top, rest } = splitTopByMetric(rows, getMetric, topN);
  if (rest.length === 0) return top as (T & { isOthers?: boolean })[];

  return [...top, { ...label(aggregate(rest), rest.length), isOthers: true }] as (T & { isOthers?: boolean })[];
}

export function aggregateCostKeys(rows: CostKeyAgg[]): CostKeyAgg {
  return rows.reduce(
    (acc, row) => ({
      key_id: "",
      spend_usd: acc.spend_usd + row.spend_usd,
      input_spend_usd: acc.input_spend_usd + (row.input_spend_usd ?? 0),
      output_spend_usd: acc.output_spend_usd + (row.output_spend_usd ?? 0),
      requests: acc.requests + row.requests,
      input_tokens: acc.input_tokens + row.input_tokens,
      output_tokens: acc.output_tokens + row.output_tokens,
    }),
    {
      key_id: "",
      spend_usd: 0,
      input_spend_usd: 0,
      output_spend_usd: 0,
      requests: 0,
      input_tokens: 0,
      output_tokens: 0,
    },
  );
}

export type SpendByKeyDisplayRow = CostKeyAgg & {
  isOthers?: boolean;
};

export function spendByKeyDisplayRows(
  rows: CostKeyAgg[],
  topN = DEFAULT_TOP_N,
  collapse = true,
): SpendByKeyDisplayRow[] {
  return topDisplayRows(
    rows,
    (row) => row.spend_usd,
    aggregateCostKeys,
    (aggregated, count) => ({
      ...aggregated,
      key_id: othersLabel(count),
    }),
    topN,
    collapse,
  );
}

export function aggregateCostUsers(rows: CostUserAgg[]): CostUserAgg {
  return rows.reduce(
    (acc, row) => ({
      scope: "",
      label: "",
      spend_usd: acc.spend_usd + row.spend_usd,
      input_spend_usd: acc.input_spend_usd + (row.input_spend_usd ?? 0),
      output_spend_usd: acc.output_spend_usd + (row.output_spend_usd ?? 0),
      requests: acc.requests + row.requests,
      input_tokens: acc.input_tokens + row.input_tokens,
      output_tokens: acc.output_tokens + row.output_tokens,
    }),
    {
      scope: "",
      label: "",
      spend_usd: 0,
      input_spend_usd: 0,
      output_spend_usd: 0,
      requests: 0,
      input_tokens: 0,
      output_tokens: 0,
    },
  );
}

export type SpendByUserDisplayRow = CostUserAgg & {
  isOthers?: boolean;
};

export function spendByUserDisplayRows(
  rows: CostUserAgg[],
  topN = DEFAULT_TOP_N,
  collapse = true,
): SpendByUserDisplayRow[] {
  return topDisplayRows(
    rows,
    (row) => row.spend_usd,
    aggregateCostUsers,
    (aggregated, count) => ({
      ...aggregated,
      scope: "__others__",
      label: othersLabel(count),
    }),
    topN,
    collapse,
  );
}

export function aggregateProviders(rows: ProviderSpendAgg[]): ProviderSpendAgg {
  return rows.reduce(
    (acc, row) => ({
      name: "",
      spend_usd: acc.spend_usd + row.spend_usd,
      requests: acc.requests + row.requests,
    }),
    { name: "", spend_usd: 0, requests: 0 },
  );
}

export type SpendByProviderDisplayRow = ProviderSpendAgg & {
  isOthers?: boolean;
};

export function spendByProviderDisplayRows(
  rows: ProviderSpendAgg[],
  topN = DEFAULT_TOP_N,
  collapse = true,
): SpendByProviderDisplayRow[] {
  return topDisplayRows(
    rows,
    (row) => row.spend_usd,
    aggregateProviders,
    (aggregated, count) => ({
      ...aggregated,
      name: othersLabel(count),
    }),
    topN,
    collapse,
  );
}

export interface UsageRow {
  scope: string;
  label: string;
  requests: number;
  tokens: number;
}

export function aggregateUsageRows(rows: UsageRow[]): UsageRow {
  return rows.reduce(
    (acc, row) => ({
      scope: "",
      label: "",
      requests: acc.requests + row.requests,
      tokens: acc.tokens + row.tokens,
    }),
    { scope: "", label: "", requests: 0, tokens: 0 },
  );
}

export type UsageDisplayRow = UsageRow & {
  isOthers?: boolean;
};

export function usageDisplayRows(
  rows: UsageRow[],
  topN = DEFAULT_TOP_N,
  collapse = true,
): UsageDisplayRow[] {
  return topDisplayRows(
    rows,
    (row) => row.tokens,
    aggregateUsageRows,
    (aggregated, count) => ({
      ...aggregated,
      scope: "__others__",
      label: othersLabel(count),
    }),
    topN,
    collapse,
  );
}

export function aggregateNameCounts(rows: NameCount[]): NameCount {
  return rows.reduce(
    (acc, row) => ({
      name: "",
      count: acc.count + row.count,
    }),
    { name: "", count: 0 },
  );
}

export type NameCountDisplayRow = NameCount & {
  isOthers?: boolean;
};

export function nameCountDisplayRows(
  rows: NameCount[],
  topN = DEFAULT_TOP_N,
  collapse = true,
): NameCountDisplayRow[] {
  return topDisplayRows(
    rows,
    (row) => row.count,
    aggregateNameCounts,
    (aggregated, count) => ({
      ...aggregated,
      name: othersLabel(count),
    }),
    topN,
    collapse,
  );
}

export interface ScopeUsageRow {
  scope: string;
  label: string;
  requests: number;
  tokens: number;
}

export function scopeUsageDisplayRows(
  rows: ScopeUsageRow[],
  topN = DEFAULT_TOP_N,
  collapse = true,
): (ScopeUsageRow & { isOthers?: boolean })[] {
  return topDisplayRows(
    rows,
    (row) => row.tokens,
    aggregateUsageRows,
    (aggregated, count) => ({
      ...aggregated,
      scope: "__others__",
      label: othersLabel(count),
    }),
    topN,
    collapse,
  );
}

export function donutSlices(
  labels: string[],
  values: number[],
  colors: string[],
  topN = 8,
  othersColor?: string,
): { labels: string[]; values: number[]; colors: string[] } {
  if (labels.length <= topN) {
    return { labels, values, colors };
  }

  const rows = labels.map((label, i) => ({ label, value: values[i] ?? 0, color: colors[i] ?? colors[0] }));
  const sorted = [...rows].sort((a, b) => b.value - a.value);
  const top = sorted.slice(0, topN);
  const rest = sorted.slice(topN);
  const restTotal = rest.reduce((sum, row) => sum + row.value, 0);

  return {
    labels: [...top.map((row) => row.label), othersLabel(rest.length)],
    values: [...top.map((row) => row.value), restTotal],
    colors: [...top.map((row) => row.color), othersColor ?? "rgba(128,128,128,0.45)"],
  };
}
