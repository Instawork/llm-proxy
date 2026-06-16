import { useMemo, useState } from "react";

import { BarChart, ChartCard, DonutChart, TrendChart } from "../components/charts";
import { chartPalette } from "../components/charts/chart-setup";
import KeyLink from "../components/ui/key-link";
import {
  type DataSource,
  LiveStat,
  RangeToggle,
  SectionPanel,
  trendChartSource,
} from "../components/ui/data-source";
import PageHeader, { ErrorAlert, LiveIndicator, LoadingBlock } from "../components/ui/page-header";
import { useKeys, useUsage } from "../hooks/queries";
import { LIVE_TREND_CHART_SUBTITLE, useHistory } from "../hooks/use-history";
import {
  DAILY_HISTORY_SUBTITLE,
  type RangeKey,
  RANGE_OPTIONS,
  aggScopeMap,
  pickToday,
  scalarSeries,
} from "../lib/daily-history";
import { compact, formatCount, scopeKind, scopeLabel } from "../lib/format";
import type { APIKey, DailyHistoryRow, UsageScopeCounter } from "../types";

function rangeLabel(range: RangeKey): string {
  return range === "today" ? "today" : `last ${range === "7d" ? "7" : "30"} days`;
}

const DONUT_COLORS = [
  chartPalette.primary,
  chartPalette.info,
  chartPalette.success,
  chartPalette.warning,
  chartPalette.error,
];

interface Row {
  scope: string;
  label: string;
  requests: number;
  tokens: number;
}

function rowsForKind(
  counters: Record<string, UsageScopeCounter> | undefined,
  kind: string,
): Row[] {
  return Object.entries(counters ?? {})
    .filter(([scope]) => scopeKind(scope) === kind)
    .map(([scope, c]) => ({
      scope,
      label: scopeLabel(scope),
      requests: c.requests ?? 0,
      tokens: c.tokens ?? 0,
    }))
    .sort((a, b) => b.tokens - a.tokens);
}

function redisRows(
  history: DailyHistoryRow[] | undefined,
  range: RangeKey,
  field: string,
): Row[] {
  return aggScopeMap(history, range, field).map((s) => ({
    scope: s.scope,
    label: scopeLabel(s.scope),
    requests: s.requests,
    tokens: s.tokens,
  }));
}

export default function UsagePage() {
  const { data, isLoading, error, dataUpdatedAt, isFetching, refetch } = useUsage();
  const keys = useKeys();
  const [range, setRange] = useState<RangeKey>("today");

  const stats = data?.stats;
  const history = stats?.daily_history;
  const hasRedis = Boolean(stats?.daily_history_available);
  const counters = stats?.counters;
  const global = counters?.["global"];

  const totalTokensPick = pickToday(
    stats?.available ? (stats?.tokens_today ?? global?.tokens) : undefined,
    history,
    "tokens_today",
    hasRedis,
  );
  const totalReqPick = pickToday(
    stats?.available ? (stats?.requests_today ?? global?.requests) : undefined,
    history,
    "requests_today",
    hasRedis,
  );
  const totalTokens = totalTokensPick.value;
  const totalRequests = totalReqPick.value;

  const tokenHistory = useHistory(stats?.available ? totalTokens : undefined);
  const dailyTokens = useMemo(() => scalarSeries(history, "tokens_today", range), [history, range]);
  const useDailyChart = Boolean(hasRedis && range !== "today" && dailyTokens.available);

  // Prefer Redis whenever it's available — including "today" and the Active
  // models / by-model / by-provider / by-key / by-user breakdowns. Memory
  // counters only reflect this one process, so on a multi-pod fleet they
  // undercount badly (the top cards already use fleet-wide today totals — using
  // memory here makes the breakdowns look like dropped requests). Fall back to
  // in-process memory only when Redis isn't wired up at all.
  const memByModel = useMemo(() => rowsForKind(counters, "model"), [counters]);
  const useRedisBreakdown = hasRedis || range !== "today";
  const breakdownSource: DataSource = useRedisBreakdown ? "redis" : "memory";

  const byModel = useMemo(
    () => (useRedisBreakdown ? redisRows(history, range, "by_model") : memByModel),
    [useRedisBreakdown, history, range, memByModel],
  );
  const byProvider = useMemo(
    () =>
      useRedisBreakdown ? redisRows(history, range, "by_provider") : rowsForKind(counters, "provider"),
    [useRedisBreakdown, history, range, counters],
  );
  const byUser = useMemo(
    () => (useRedisBreakdown ? redisRows(history, range, "by_user") : rowsForKind(counters, "user")),
    [useRedisBreakdown, history, range, counters],
  );
  const byKey = useMemo(
    () => (useRedisBreakdown ? redisRows(history, range, "by_key") : rowsForKind(counters, "key")),
    [useRedisBreakdown, history, range, counters],
  );
  const donutTokens = byProvider.reduce((sum, r) => sum + r.tokens, 0);

  if (isLoading) return <LoadingBlock />;
  if (error) {
    return <ErrorAlert message={error instanceof Error ? error.message : "Failed to load usage"} />;
  }

  const avgTokens = totalRequests > 0 ? Math.round(totalTokens / totalRequests) : 0;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Usage"
        description="Request and token volume by model, provider, and user (UTC day rollup from cost tracking)."
        actions={
          <div className="flex items-center gap-3">
            <RangeToggle value={range} options={RANGE_OPTIONS} onChange={setRange} />
            <LiveIndicator updatedAt={dataUpdatedAt} fetching={isFetching} onRefresh={() => refetch()} />
          </div>
        }
      />

      {!data?.enabled ? (
        <div className="alert alert-info">
          <span>
            Usage stats require <code className="mx-1">features.cost_tracking</code> — enable it and restart the
            proxy.
          </span>
        </div>
      ) : null}

      {!stats?.available ? (
        <div className="alert alert-info">
          <span>Live usage stats are inactive — no tracked requests yet today.</span>
        </div>
      ) : null}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <LiveStat
          title="Requests"
          value={compact(totalRequests)}
          hint={stats?.day ?? "today"}
          source={totalReqPick.source}
        />
        <LiveStat title="Tokens" value={compact(totalTokens)} hint="UTC day" source={totalTokensPick.source} />
        <LiveStat title="Avg tokens / req" value={compact(avgTokens)} source={totalTokensPick.source} />
        <LiveStat
          title="Active models"
          value={byModel.length}
          hint={`${byProvider.length} providers · ${rangeLabel(range)}`}
          source={breakdownSource}
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <ChartCard
            title="Token volume"
            subtitle={useDailyChart ? DAILY_HISTORY_SUBTITLE : LIVE_TREND_CHART_SUBTITLE}
            source={trendChartSource(useDailyChart)}
          >
            {useDailyChart ? (
              <BarChart
                labels={dailyTokens.labels}
                values={dailyTokens.values}
                label="Daily tokens"
                colors={dailyTokens.labels.map(() => chartPalette.primary())}
              />
            ) : (
              <TrendChart points={tokenHistory} label="Tokens" color={chartPalette.primary()} />
            )}
          </ChartCard>
        </div>
        <ChartCard
          title="Tokens by provider"
          subtitle={`Share of total · ${rangeLabel(range)}`}
          source={breakdownSource}
        >
          <DonutChart
            labels={byProvider.map((r) => r.label)}
            values={byProvider.map((r) => r.tokens)}
            colors={byProvider.map((_, i) => DONUT_COLORS[i % DONUT_COLORS.length]())}
            centerValue={compact(donutTokens)}
            centerLabel="tokens"
          />
        </ChartCard>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <ChartCard title="Requests by model" subtitle={`Top models · ${rangeLabel(range)}`} source={breakdownSource}>
          <BarChart
            labels={byModel.slice(0, 8).map((r) => r.label)}
            values={byModel.slice(0, 8).map((r) => r.requests)}
            label="Requests"
            colors={byModel.slice(0, 8).map(() => chartPalette.primary())}
            horizontal
          />
        </ChartCard>
        <ChartCard title="Tokens by model" subtitle={`Top models · ${rangeLabel(range)}`} source={breakdownSource}>
          <BarChart
            labels={byModel.slice(0, 8).map((r) => r.label)}
            values={byModel.slice(0, 8).map((r) => r.tokens)}
            label="Tokens"
            colors={byModel.slice(0, 8).map(() => chartPalette.info())}
            horizontal
          />
        </ChartCard>
      </div>

      <UsageTable title="By model" rows={byModel} source={breakdownSource} range={range} />
      <UsageTable title="By key" rows={byKey} keys={keys.data} linkKeys source={breakdownSource} range={range} />
      <UsageTable title="By user" rows={byUser} source={breakdownSource} range={range} />
    </div>
  );
}

function UsageTable({
  title,
  rows,
  keys,
  linkKeys = false,
  source,
  range,
}: {
  title: string;
  rows: Row[];
  keys?: APIKey[];
  linkKeys?: boolean;
  source: DataSource;
  range: RangeKey;
}) {
  const totalTokens = rows.reduce((s, r) => s + r.tokens, 0);
  const subtitle =
    source === "redis" ? `Summed Redis rollups · ${rangeLabel(range)}` : `Live memory · ${rangeLabel(range)}`;
  return (
    <SectionPanel title={title} subtitle={subtitle} source={source}>
      <div className="overflow-x-auto">
        <table className="table table-zebra">
          <thead>
            <tr>
              <th>{title.replace("By ", "")}</th>
              <th className="text-right">Requests</th>
              <th className="text-right">Tokens</th>
              <th className="text-right">Share</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.scope}>
                <td className="font-medium">
                  {linkKeys ? (
                    <KeyLink keys={keys} scope={r.scope} label={r.label} />
                  ) : (
                    r.label
                  )}
                </td>
                <td className="text-right">{formatCount(r.requests)}</td>
                <td className="text-right">{formatCount(r.tokens)}</td>
                <td className="text-right text-base-content/60">
                  {totalTokens > 0 ? `${((r.tokens / totalTokens) * 100).toFixed(1)}%` : "—"}
                </td>
              </tr>
            ))}
            {rows.length === 0 ? (
              <tr>
                <td colSpan={4} className="text-center text-base-content/50">
                  No usage recorded today
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </SectionPanel>
  );
}
