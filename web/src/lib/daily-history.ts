import type { DailyHistoryRow } from "../types";

export const DAILY_HISTORY_SUBTITLE =
  "Daily UTC totals from Redis — survives proxy restarts (today updates live)";

export type RangeKey = "today" | "7d" | "30d";

export const RANGE_OPTIONS: { key: RangeKey; label: string }[] = [
  { key: "today", label: "Today" },
  { key: "7d", label: "7 days" },
  { key: "30d", label: "30 days" },
];

export function rangeDays(range: RangeKey): number {
  if (range === "today") return 1;
  if (range === "7d") return 7;
  return 30;
}

function todayUTC(): string {
  return new Date().toISOString().slice(0, 10);
}

function asNum(value: unknown): number {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string") {
    const n = Number(value);
    return Number.isFinite(n) ? n : 0;
  }
  return 0;
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function asArray(value: unknown): Record<string, unknown>[] {
  return Array.isArray(value) ? (value as Record<string, unknown>[]) : [];
}

function sortRows(rows: DailyHistoryRow[]): DailyHistoryRow[] {
  return [...rows].sort((a, b) => a.day.localeCompare(b.day));
}

/** Most-recent (today) row, if present. */
export function latestRow(rows: DailyHistoryRow[] | undefined): DailyHistoryRow | undefined {
  if (!rows?.length) return undefined;
  return sortRows(rows)[rows.length - 1];
}

/** The row for the current UTC day, if Redis has it. */
export function todayRow(rows: DailyHistoryRow[] | undefined): DailyHistoryRow | undefined {
  return rows?.find((r) => r.day === todayUTC());
}

/** Last N days of rows for the selected range (oldest-first). */
export function sliceRange(
  rows: DailyHistoryRow[] | undefined,
  range: RangeKey,
): DailyHistoryRow[] {
  if (!rows?.length) return [];
  const sorted = sortRows(rows);
  if (range === "today") {
    const t = sorted.filter((r) => r.day === todayUTC());
    return t.length ? t : sorted.slice(-1);
  }
  return sorted.slice(-rangeDays(range));
}

/** Scalar series for a trend chart over a window (default 30d). */
export function scalarSeries(
  rows: DailyHistoryRow[] | undefined,
  field: string,
  range: RangeKey = "30d",
): { labels: string[]; values: number[]; available: boolean } {
  const slice = sliceRange(rows, range === "today" ? "30d" : range);
  if (!slice.length) return { labels: [], values: [], available: false };
  return {
    labels: slice.map((r) => r.day.slice(5)),
    values: slice.map((r) => asNum(r[field])),
    available: true,
  };
}

/** Backwards-compatible scalar chart over full history. */
export function dailyHistoryChart(
  rows: DailyHistoryRow[] | undefined,
  field: string,
): { labels: string[]; values: number[]; available: boolean } {
  return scalarSeries(rows, field, "30d");
}

export type ValueSource = "memory" | "redis" | "redislive";

/**
 * Resolves a "today" scalar and its honest data source.
 *
 * The badge reflects config + functionality, not whether the counter happens
 * to be non-zero:
 *   - rollups ON  → the value is archived to Redis each day, so it survives a
 *     restart. Normal case is "redislive" (live counter, also persisted); if
 *     memory has reset to 0 but Redis still holds today's value we serve that
 *     and label "redis".
 *   - rollups OFF → purely in-process, so "memory".
 */
export function pickToday(
  memoryValue: number | undefined,
  rows: DailyHistoryRow[] | undefined,
  field: string,
  redisAvailable = false,
): { value: number; source: ValueSource } {
  const mem = memoryValue ?? 0;
  const row = todayRow(rows);
  const redisVal = row ? asNum(row[field]) : 0;

  if (!redisAvailable) {
    return { value: mem, source: "memory" };
  }
  if (mem > 0) return { value: mem, source: "redislive" };
  if (redisVal > 0) return { value: redisVal, source: "redis" };
  // Wired to Redis, just no data for today yet — still durable.
  return { value: mem, source: "redislive" };
}

// --- Cost breakdowns (arrays of objects) ------------------------------------

export interface CostKeyAgg {
  key_id: string;
  spend_usd: number;
  input_spend_usd: number;
  output_spend_usd: number;
  requests: number;
  input_tokens: number;
  output_tokens: number;
}

export function aggCostByKey(
  rows: DailyHistoryRow[] | undefined,
  range: RangeKey,
): CostKeyAgg[] {
  const acc = new Map<string, CostKeyAgg>();
  for (const row of sliceRange(rows, range)) {
    for (const raw of asArray(row.by_key)) {
      const id = String(raw.key_id ?? "");
      if (!id) continue;
      const cur = acc.get(id) ?? {
        key_id: id,
        spend_usd: 0,
        input_spend_usd: 0,
        output_spend_usd: 0,
        requests: 0,
        input_tokens: 0,
        output_tokens: 0,
      };
      cur.spend_usd += asNum(raw.spend_usd);
      cur.input_spend_usd += asNum(raw.input_spend_usd);
      cur.output_spend_usd += asNum(raw.output_spend_usd);
      cur.requests += asNum(raw.requests);
      cur.input_tokens += asNum(raw.input_tokens);
      cur.output_tokens += asNum(raw.output_tokens);
      acc.set(id, cur);
    }
  }
  return [...acc.values()].sort((a, b) => b.spend_usd - a.spend_usd);
}

export interface ProviderSpendAgg {
  name: string;
  spend_usd: number;
  requests: number;
}

export function aggCostByProvider(
  rows: DailyHistoryRow[] | undefined,
  range: RangeKey,
): ProviderSpendAgg[] {
  const acc = new Map<string, ProviderSpendAgg>();
  for (const row of sliceRange(rows, range)) {
    for (const raw of asArray(row.by_provider)) {
      const name = String(raw.name ?? "");
      if (!name) continue;
      const cur = acc.get(name) ?? { name, spend_usd: 0, requests: 0 };
      cur.spend_usd += asNum(raw.spend_usd);
      cur.requests += asNum(raw.requests);
      acc.set(name, cur);
    }
  }
  return [...acc.values()].sort((a, b) => b.spend_usd - a.spend_usd);
}

/** Per-day spend for one masked key (for the key-detail sparkline). */
export function costSeriesForKey(
  rows: DailyHistoryRow[] | undefined,
  maskedKey: string,
  range: RangeKey = "7d",
): { labels: string[]; values: number[]; available: boolean } {
  const slice = sliceRange(rows, range === "today" ? "7d" : range);
  if (!slice.length || !maskedKey) return { labels: [], values: [], available: false };
  const values = slice.map((row) => {
    const match = asArray(row.by_key).find((r) => String(r.key_id ?? "") === maskedKey);
    return match ? asNum(match.spend_usd) : 0;
  });
  return { labels: slice.map((r) => r.day.slice(5)), values, available: true };
}

// --- Usage breakdowns (scope maps) ------------------------------------------

export interface ScopeAgg {
  scope: string;
  requests: number;
  tokens: number;
}

export function aggScopeMap(
  rows: DailyHistoryRow[] | undefined,
  range: RangeKey,
  field: string,
): ScopeAgg[] {
  const acc = new Map<string, ScopeAgg>();
  for (const row of sliceRange(rows, range)) {
    const map = asRecord(row[field]);
    for (const [scope, raw] of Object.entries(map)) {
      const rec = asRecord(raw);
      const cur = acc.get(scope) ?? { scope, requests: 0, tokens: 0 };
      cur.requests += asNum(rec.requests ?? rec.Requests);
      cur.tokens += asNum(rec.tokens ?? rec.Tokens);
      acc.set(scope, cur);
    }
  }
  return [...acc.values()].sort((a, b) => b.tokens - a.tokens);
}

// --- name/count arrays (PII) ------------------------------------------------

export interface NameCount {
  name: string;
  count: number;
}

export function aggNameCount(
  rows: DailyHistoryRow[] | undefined,
  range: RangeKey,
  field: string,
): NameCount[] {
  const acc = new Map<string, number>();
  for (const row of sliceRange(rows, range)) {
    for (const raw of asArray(row[field])) {
      const name = String(raw.name ?? "");
      if (!name) continue;
      acc.set(name, (acc.get(name) ?? 0) + asNum(raw.count));
    }
  }
  return [...acc.entries()]
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count);
}

// --- Circuit providers (map name -> {failures}) -----------------------------

export function aggCircuitProviders(
  rows: DailyHistoryRow[] | undefined,
  range: RangeKey,
): NameCount[] {
  // Circuit failures are a rolling-window gauge, not a per-day total, so we
  // take the PEAK observed per provider across the range rather than summing
  // (summing point-in-time gauge samples is meaningless and double-counts).
  const acc = new Map<string, number>();
  for (const row of sliceRange(rows, range)) {
    const map = asRecord(row.providers);
    for (const [name, raw] of Object.entries(map)) {
      acc.set(name, Math.max(acc.get(name) ?? 0, asNum(asRecord(raw).failures)));
    }
  }
  return [...acc.entries()]
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count);
}

/** Stacked per-provider failures over time (one dataset per provider). */
export function circuitProviderSeries(
  rows: DailyHistoryRow[] | undefined,
  range: RangeKey,
): { labels: string[]; providers: string[]; valuesByProvider: Record<string, number[]> } {
  const slice = sliceRange(rows, range === "today" ? "7d" : range);
  const providerSet = new Set<string>();
  for (const row of slice) {
    for (const name of Object.keys(asRecord(row.providers))) providerSet.add(name);
  }
  const providers = [...providerSet];
  const valuesByProvider: Record<string, number[]> = {};
  for (const name of providers) {
    valuesByProvider[name] = slice.map((row) =>
      asNum(asRecord(asRecord(row.providers)[name]).failures),
    );
  }
  return { labels: slice.map((r) => r.day.slice(5)), providers, valuesByProvider };
}
