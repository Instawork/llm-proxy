import { useMemo, useState } from "react";

import SpendByKeyTable from "../components/cost/spend-by-key-table";
import SpendByProviderTable from "../components/cost/spend-by-provider-table";
import { LimitSpendTable, RecentCostTable, TransportsTable } from "../components/cost/extra-tables";
import { BarChart, ChartCard, DonutChart, GroupedBarChart, TrendChart } from "../components/charts";
import { chartPalette } from "../components/charts/chart-setup";
import {
  type DataSource,
  LiveStat,
  RangeToggle,
  SectionPanel,
  trendChartSource,
} from "../components/ui/data-source";
import PageHeader, { ErrorAlert, LiveIndicator, LoadingBlock } from "../components/ui/page-header";
import { useCost, useKeys } from "../hooks/queries";
import { LIVE_TREND_CHART_SUBTITLE, useHistory } from "../hooks/use-history";
import {
  type CostKeyAgg,
  DAILY_HISTORY_SUBTITLE,
  type RangeKey,
  RANGE_OPTIONS,
  aggCostByKey,
  aggCostByProvider,
  pickToday,
  scalarSeries,
} from "../lib/daily-history";
import { compact, formatCount, formatUsd, keySpendCapCents, maskKeyId } from "../lib/format";
import { donutSlices } from "../lib/group-rows";
import type { CostKeySpend } from "../types";

function rangeLabel(range: RangeKey): string {
  return range === "today" ? "today" : `last ${range === "7d" ? "7" : "30"} days`;
}

function memKeyAgg(byKey: CostKeySpend[]): CostKeyAgg[] {
  return byKey.map((row) => ({
    key_id: row.key_id ?? "",
    spend_usd: row.spend_usd,
    input_spend_usd: row.input_spend_usd ?? 0,
    output_spend_usd: row.output_spend_usd ?? 0,
    requests: row.requests,
    input_tokens: row.input_tokens,
    output_tokens: row.output_tokens,
  }));
}

const SPEND_COLORS = [
  chartPalette.primary,
  chartPalette.info,
  chartPalette.success,
  chartPalette.warning,
  chartPalette.error,
];

function spendByMaskedKey(byKey: CostKeySpend[] | undefined, key: string): number {
  const masked = maskKeyId(key);
  const entry = byKey?.find((row) => row.key_id === masked);
  return entry?.spend_usd ?? 0;
}

export default function CostPage() {
  const { data, isLoading, error, dataUpdatedAt, isFetching, refetch } = useCost();
  const keys = useKeys();
  const [range, setRange] = useState<RangeKey>("today");

  const stats = data?.stats;
  const history = stats?.daily_history;
  const hasRedis = Boolean(stats?.daily_history_available);

  const spendToday = pickToday(
    stats?.available ? stats?.spend_today_usd : undefined,
    history,
    "spend_today_usd",
    hasRedis,
  );
  const requestsToday = pickToday(
    stats?.available ? stats?.requests_today : undefined,
    history,
    "requests_today",
    hasRedis,
  );
  const tokensSource: DataSource = hasRedis ? "redislive" : "memory";
  const inputSpendToday = stats?.input_spend_today_usd ?? 0;
  const outputSpendToday = stats?.output_spend_today_usd ?? 0;

  const spendHistory = useHistory(stats?.available ? spendToday.value : undefined);
  const dailySpend = useMemo(
    () => scalarSeries(history, "spend_today_usd", range),
    [history, range],
  );
  const useDailyChart = Boolean(hasRedis && range !== "today" && dailySpend.available);

  if (isLoading) return <LoadingBlock />;
  if (error) {
    return <ErrorAlert message={error instanceof Error ? error.message : "Failed to load cost tracking"} />;
  }
  if (!data) return null;

  const transports = data.transports ?? [];
  const keyList = keys.data ?? [];
  const byKey = stats?.by_key ?? [];
  const recent = stats?.recent ?? [];
  const withLimits = keyList.filter((k) => keySpendCapCents(k) > 0);

  // Prefer fleet-wide Redis rollups whenever Redis is available — including
  // "today" — since in-process memory only reflects this one pod and undercounts
  // on a multi-pod fleet. Fall back to memory only when Redis isn't wired up.
  const useRedisBreakdown = hasRedis || range !== "today";
  const memKeys = memKeyAgg(byKey);
  const rangeByKey: CostKeyAgg[] = useRedisBreakdown ? aggCostByKey(history, range) : memKeys;
  const breakdownSource: DataSource = useRedisBreakdown ? "redis" : "memory";
  const withSpend = rangeByKey.filter((row) => row.spend_usd > 0);
  const rangeSpendTotal = withSpend.reduce((sum, row) => sum + row.spend_usd, 0);
  const donutData = donutSlices(
    withSpend.map((row) => row.key_id || "unknown"),
    withSpend.map((row) => row.spend_usd),
    withSpend.map((_, i) => SPEND_COLORS[i % SPEND_COLORS.length]()),
    8,
    chartPalette.tick(),
  );

  const memProviders = stats?.by_provider ?? [];
  const rangeByProvider = useRedisBreakdown
    ? aggCostByProvider(history, range)
    : memProviders.map((p) => ({ name: p.name, spend_usd: p.spend_usd, requests: p.requests }));
  const withProviderSpend = rangeByProvider.filter((row) => row.spend_usd > 0);

  const limitRows = keyList
    .map((key) => ({
      id: key.key,
      label: key.description || maskKeyId(key.key),
      key,
      spendUsd: spendByMaskedKey(byKey, key.key),
      limitUsd: keySpendCapCents(key) / 100,
      requests: byKey.find((entry) => entry.key_id === maskKeyId(key.key))?.requests ?? 0,
    }))
    .filter((row) => row.limitUsd > 0 || row.spendUsd > 0)
    .sort((a, b) => b.spendUsd - a.spendUsd || b.limitUsd - a.limitUsd);

  const limitChartRows = limitRows.filter((row) => row.limitUsd > 0).slice(0, 8);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Cost Tracking"
        description="Live spend rollup (since last restart, UTC calendar day) plus pipeline configuration and per-key limits."
        actions={
          <div className="flex items-center gap-3">
            <RangeToggle value={range} options={RANGE_OPTIONS} onChange={setRange} />
            <LiveIndicator updatedAt={dataUpdatedAt} fetching={isFetching} onRefresh={() => refetch()} />
          </div>
        }
      />

      {!data.enabled ? (
        <div className="alert">
          <span>Cost tracking is disabled.</span>
        </div>
      ) : null}

      {!stats?.available ? (
        <div className="alert alert-info">
          <span>
            Live spend stats are inactive — enable <code className="mx-1">features.cost_tracking</code> and restart
            the proxy.
          </span>
        </div>
      ) : null}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <LiveStat
          title="Spend today"
          value={formatUsd(spendToday.value)}
          hint={`${formatUsd(inputSpendToday)} in · ${formatUsd(outputSpendToday)} out`}
          source={spendToday.source}
        />
        <LiveStat
          title="Requests"
          value={formatCount(requestsToday.value)}
          hint="tracked today"
          source={requestsToday.source}
        />
        <LiveStat
          title="Tokens"
          value={compact((stats?.input_tokens_today ?? 0) + (stats?.output_tokens_today ?? 0))}
          hint={`${compact(stats?.input_tokens_today)} in · ${compact(stats?.output_tokens_today)} out`}
          source={tokensSource}
        />
        <LiveStat
          title="Pipeline"
          value={data.async ? "Async" : "Sync"}
          hint={`${transports.length} transport${transports.length === 1 ? "" : "s"} · ${withLimits.length} keys w/ limits`}
          source="config"
          valueClassName="text-lg"
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <ChartCard
            title="Spend over time"
            subtitle={useDailyChart ? DAILY_HISTORY_SUBTITLE : LIVE_TREND_CHART_SUBTITLE}
            source={trendChartSource(useDailyChart)}
          >
            {useDailyChart ? (
              <BarChart
                labels={dailySpend.labels}
                values={dailySpend.values}
                label="Daily spend (USD)"
                colors={dailySpend.labels.map(() => chartPalette.primary())}
              />
            ) : (
              <TrendChart points={spendHistory} label="Spend today (USD)" color={chartPalette.primary()} />
            )}
          </ChartCard>
        </div>
        <ChartCard
          title="Spend by key"
          subtitle={`Share of ${rangeLabel(range)}'s spend`}
          source={breakdownSource}
        >
          <DonutChart
            labels={donutData.labels}
            values={donutData.values}
            colors={donutData.colors}
            centerValue={formatUsd(rangeSpendTotal)}
            centerLabel={rangeLabel(range)}
          />
        </ChartCard>
      </div>

      {withSpend.length > 0 || withProviderSpend.length > 0 ? (
        <div className="grid gap-4 lg:grid-cols-2">
          {withSpend.length > 0 ? (
            <SectionPanel
              title="Spend by key"
              subtitle={
                range === "today"
                  ? `Rollup for ${stats?.day ?? "today"}`
                  : `Summed Redis rollups · ${rangeLabel(range)}`
              }
              source={breakdownSource}
            >
              <SpendByKeyTable rows={withSpend} keys={keyList} />
            </SectionPanel>
          ) : null}
          {withProviderSpend.length > 0 ? (
            <SectionPanel
              title="Spend by provider"
              subtitle={`Tracked spend · ${rangeLabel(range)}`}
              source={breakdownSource}
            >
              <SpendByProviderTable rows={withProviderSpend} />
            </SectionPanel>
          ) : null}
        </div>
      ) : null}

      <ChartCard
        title="Spend vs limit"
        subtitle="Spend is today's rollup; caps from key config (DynamoDB)"
        source={breakdownSource}
      >
        <GroupedBarChart
          labels={limitChartRows.map((row) => row.key.description || maskKeyId(row.key.key))}
          series={[
            {
              label: "Spend today",
              values: limitChartRows.map((row) => row.spendUsd),
              color: chartPalette.primary,
            },
            {
              label: "Spend cap",
              values: limitChartRows.map((row) => row.limitUsd),
              color: chartPalette.info,
            },
          ]}
          horizontal
          height={Math.max(220, limitChartRows.length * 36)}
        />
      </ChartCard>

      <SectionPanel title="Async pipeline" source="config">
        <div className="grid gap-4 p-5 sm:grid-cols-2">
          <Field label="Workers" value={data.workers} />
          <Field label="Queue size" value={data.queue_size} />
          <Field label="Flush interval" value={data.flush_interval ? `${data.flush_interval}s` : undefined} />
          <Field label="Transports" value={data.transport_count ?? transports.length} />
        </div>
      </SectionPanel>

      <SectionPanel
        title="Per-key spend vs limit"
        subtitle="Spend is today's rollup; caps are stored on the key (DynamoDB)"
        source={breakdownSource}
      >
        <LimitSpendTable rows={limitRows} keys={keyList} />
      </SectionPanel>

      {recent.length > 0 ? (
        <SectionPanel
          title="Recent tracked requests"
          subtitle="Last 50 events — not written to Redis"
          source="memory"
        >
          <RecentCostTable rows={recent} keys={keyList} />
        </SectionPanel>
      ) : null}

      <SectionPanel title="Configured transports" subtitle="Cost audit pipeline (file / DynamoDB / Datadog)" source="config">
        <TransportsTable transports={transports} />
      </SectionPanel>
    </div>
  );
}

function Field({ label, value }: { label: string; value?: React.ReactNode }) {
  return (
    <div>
      <p className="text-xs uppercase tracking-wide text-base-content/50">{label}</p>
      <p className="text-lg font-medium">{value ?? "—"}</p>
    </div>
  );
}
