import { useMemo, useState } from "react";

import { BarChart, ChartCard, GroupedBarChart, TrendChart } from "../components/charts";
import { chartPalette } from "../components/charts/chart-setup";
import {
  circuitLiveSource,
  LiveStat,
  RangeToggle,
  SectionPanel,
  trendChartSource,
} from "../components/ui/data-source";
import PageHeader, {
  ErrorAlert,
  LiveIndicator,
  LoadingBlock,
  ProviderBadge,
  StatusBadge,
} from "../components/ui/page-header";
import { useHealth } from "../hooks/queries";
import { LIVE_TREND_CHART_SUBTITLE, useHistory } from "../hooks/use-history";
import {
  DAILY_HISTORY_SUBTITLE,
  type RangeKey,
  RANGE_OPTIONS,
  circuitProviderSeries,
  scalarSeries,
} from "../lib/daily-history";

const PROVIDER_SERIES_COLORS = [
  chartPalette.error,
  chartPalette.warning,
  chartPalette.info,
  chartPalette.primary,
  chartPalette.success,
];

function stateColor(state: string): string {
  if (state === "open") return chartPalette.error();
  if (state === "half-open" || state === "half_open") return chartPalette.warning();
  return chartPalette.success();
}

function rangeLabel(range: RangeKey): string {
  return range === "today" ? "today" : `last ${range === "7d" ? "7" : "30"} days`;
}

export default function CircuitPage() {
  const { data, isLoading, error, dataUpdatedAt, isFetching, refetch } = useHealth();
  const [range, setRange] = useState<RangeKey>("today");

  const providers = data?.circuit_breaker?.providers ?? {};
  const names = Object.keys(providers);
  const cb = data?.circuit_breaker;
  const history = cb?.daily_history;
  const hasRedis = Boolean(cb?.daily_history_available);
  const totalFailures =
    cb?.total_failures ?? Object.values(providers).reduce((s, p) => s + (p.failures ?? 0), 0);
  const failureHistory = useHistory(data ? totalFailures : undefined);
  const dailyFailures = useMemo(
    () => scalarSeries(history, "total_failures", range),
    [history, range],
  );
  const useDailyChart = Boolean(hasRedis && range !== "today" && dailyFailures.available);

  const providerSeries = useMemo(() => circuitProviderSeries(history, range), [history, range]);
  const showProviderHistory = hasRedis && range !== "today" && providerSeries.providers.length > 0;

  const liveSource = circuitLiveSource(cb?.backend);

  if (isLoading) return <LoadingBlock />;
  if (error) {
    return <ErrorAlert message={error instanceof Error ? error.message : "Failed to load circuit breaker"} />;
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Circuit Breaker"
        description="Per-provider failure tracking and trip state."
        actions={
          <div className="flex items-center gap-3">
            <RangeToggle value={range} options={RANGE_OPTIONS} onChange={setRange} />
            <LiveIndicator updatedAt={dataUpdatedAt} fetching={isFetching} onRefresh={() => refetch()} />
          </div>
        }
      />

      {!cb?.enabled ? (
        <div className="alert">
          <span>Circuit breaker is disabled.</span>
        </div>
      ) : null}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <LiveStat title="Enabled" value={cb?.enabled ? "Yes" : "No"} source="config" />
        <LiveStat title="Mode" value={cb?.mode ?? "—"} source="config" valueClassName="text-lg" />
        <LiveStat
          title="Backend"
          value={
            <>
              {cb?.backend ?? "—"}
              {cb?.redis_fallback ? " (fallback)" : ""}
            </>
          }
          hint="Live breaker state store"
          source={liveSource}
          valueClassName="text-lg"
        />
        <LiveStat title="Total failures" value={totalFailures} hint="Current window" source={liveSource} />
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <ChartCard
          title="Failure trend"
          subtitle={useDailyChart ? DAILY_HISTORY_SUBTITLE : LIVE_TREND_CHART_SUBTITLE}
          source={trendChartSource(useDailyChart)}
        >
          {useDailyChart ? (
            <BarChart
              labels={dailyFailures.labels}
              values={dailyFailures.values}
              label="Daily failures"
              colors={dailyFailures.labels.map(() => chartPalette.error())}
            />
          ) : (
            <TrendChart points={failureHistory} label="Failures" color={chartPalette.error()} />
          )}
        </ChartCard>
        <ChartCard title="Failures by provider" subtitle="Current count" source={liveSource}>
          <BarChart
            labels={names}
            values={names.map((n) => providers[n].failures ?? 0)}
            label="Failures"
            colors={names.map((n) => stateColor(providers[n].state ?? "closed"))}
          />
        </ChartCard>
      </div>

      {showProviderHistory ? (
        <ChartCard
          title="Failures by provider over time"
          subtitle={`Daily peak failures (rolling window) · ${rangeLabel(range)} · ${DAILY_HISTORY_SUBTITLE}`}
          source="redis"
        >
          <GroupedBarChart
            stacked
            labels={providerSeries.labels}
            series={providerSeries.providers.map((name, i) => ({
              label: name,
              values: providerSeries.valuesByProvider[name],
              color: PROVIDER_SERIES_COLORS[i % PROVIDER_SERIES_COLORS.length],
            }))}
            height={260}
          />
        </ChartCard>
      ) : null}

      <SectionPanel title="Providers" subtitle="Live trip state and failure counters" source={liveSource}>
        <div className="overflow-x-auto">
          <table className="table table-zebra">
            <thead>
              <tr>
                <th>Provider</th>
                <th>State</th>
                <th>Failures</th>
                <th>Rollup</th>
                <th>Threshold</th>
              </tr>
            </thead>
            <tbody>
              {names.map((name) => {
                const p = providers[name];
                const state = p.state ?? p.error ?? "unknown";
                return (
                  <tr key={name}>
                    <td>
                      <ProviderBadge provider={name} />
                    </td>
                    <td>
                      <StatusBadge
                        active={state === "closed"}
                        activeLabel="closed"
                        inactiveLabel={state}
                      />
                    </td>
                    <td>{p.failures ?? "—"}</td>
                    <td>{p.rollup?.open ? "open" : p.rollup?.enabled ? "closed" : "—"}</td>
                    <td>{p.rollup?.threshold ?? "—"}</td>
                  </tr>
                );
              })}
              {names.length === 0 ? (
                <tr>
                  <td colSpan={5} className="text-center text-base-content/50">
                    No provider data
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </SectionPanel>
    </div>
  );
}
