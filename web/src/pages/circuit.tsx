import { useMemo, useState } from "react";

import { BarChart, ChartCard, GroupedBarChart, TrendChart } from "../components/charts";
import { chartPalette } from "../components/charts/chart-setup";
import {
  circuitLiveSource,
  LiveStat,
  RangeToggle,
  SectionPanel,
  trendChartSource,
  type DataSource,
} from "../components/ui/data-source";
import PageHeader, {
  ErrorAlert,
  LiveIndicator,
  LoadingBlock,
  ProviderBadge,
  StatusBadge,
} from "../components/ui/page-header";
import { useHealth, useCircuitActivity } from "../hooks/queries";
import type { CircuitActivityEvent, DailyHistoryRow } from "../types";
import { LIVE_TREND_CHART_SUBTITLE, useHistory } from "../hooks/use-history";
import {
  aggCircuitActivity,
  aggCircuitProviders,
  DAILY_HISTORY_SUBTITLE,
  maxScalarField,
  pickToday,
  type RangeKey,
  RANGE_OPTIONS,
  rangeStartUnix,
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

function eventLabel(kind: string): string {
  switch (kind) {
    case "probe":
      return "Recovery probe";
    case "probe_closed":
      return "Closed (recovered)";
    case "probe_reopened":
      return "Re-opened";
    case "fast_fail":
      return "Blocked (open)";
    case "opened":
      return "Tripped open";
    default:
      return kind;
  }
}

function formatEventTime(unix: number): string {
  return new Date(unix * 1000).toLocaleString();
}

function activityFieldPick(
  range: RangeKey,
  field: keyof ReturnType<typeof aggCircuitActivity>,
  live: number | undefined,
  history: DailyHistoryRow[] | undefined,
  hasRedis: boolean,
) {
  if (range === "today") {
    return pickToday(live, history, field, hasRedis);
  }
  if (hasRedis && history?.length) {
    return { value: aggCircuitActivity(history, range)[field], source: "redis" as const };
  }
  return { value: live ?? 0, source: "memory" as const };
}

export default function CircuitPage() {
  const { data, isLoading, error, dataUpdatedAt, isFetching, refetch } = useHealth();
  const activityQuery = useCircuitActivity();
  const [range, setRange] = useState<RangeKey>("today");

  const activity = activityQuery.data;
  const allRecentEvents = activity?.recent_events ?? [];

  const providers = data?.circuit_breaker?.providers ?? {};
  const names = Object.keys(providers);
  const cb = data?.circuit_breaker;
  const failureHistory = cb?.daily_history;
  const hasFailureRedis = Boolean(cb?.daily_history_available);

  const activityHistory = activity?.daily_history;
  const hasActivityRedis = Boolean(activity?.daily_history_available);

  const liveTotalFailures =
    cb?.total_failures ?? Object.values(providers).reduce((s, p) => s + (p.failures ?? 0), 0);

  const totalFailures =
    range === "today"
      ? liveTotalFailures
      : hasFailureRedis
        ? maxScalarField(failureHistory, range, "total_failures")
        : liveTotalFailures;

  const failureStatSource: DataSource =
    range === "today" ? circuitLiveSource(cb?.backend) : hasFailureRedis ? "redis" : "memory";

  const failureStatHint =
    range === "today" ? "Current 120s window" : `Peak daily window · ${rangeLabel(range)}`;

  const providerFailurePeaks = useMemo(() => {
    const map = new Map<string, number>();
    if (range !== "today" && hasFailureRedis) {
      for (const row of aggCircuitProviders(failureHistory, range)) {
        map.set(row.name, row.count);
      }
    }
    return map;
  }, [range, hasFailureRedis, failureHistory]);

  const providerFailureValues = names.map((n) =>
    range === "today" || !hasFailureRedis
      ? (providers[n].failures ?? 0)
      : (providerFailurePeaks.get(n) ?? 0),
  );

  const failureHistoryTrend = useHistory(range === "today" ? liveTotalFailures : undefined);
  const dailyFailures = useMemo(
    () => scalarSeries(failureHistory, "total_failures", range),
    [failureHistory, range],
  );
  const useDailyFailureChart = Boolean(hasFailureRedis && range !== "today" && dailyFailures.available);

  const providerSeries = useMemo(() => circuitProviderSeries(failureHistory, range), [failureHistory, range]);
  const showProviderHistory = hasFailureRedis && range !== "today" && providerSeries.providers.length > 0;

  const checksPick = activityFieldPick(
    range,
    "checks_total",
    activity?.checks_total,
    activityHistory,
    hasActivityRedis,
  );
  const blockedPick = activityFieldPick(
    range,
    "blocked_open",
    activity?.blocked_open,
    activityHistory,
    hasActivityRedis,
  );
  const probesPick = activityFieldPick(
    range,
    "probes_started",
    activity?.probes_started,
    activityHistory,
    hasActivityRedis,
  );
  const probesOkPick = activityFieldPick(
    range,
    "probes_succeeded",
    activity?.probes_succeeded,
    activityHistory,
    hasActivityRedis,
  );
  const probesFailPick = activityFieldPick(
    range,
    "probes_failed",
    activity?.probes_failed,
    activityHistory,
    hasActivityRedis,
  );

  const dailyChecks = useMemo(
    () => scalarSeries(activityHistory, "checks_total", range),
    [activityHistory, range],
  );
  const useDailyActivityChart = Boolean(
    hasActivityRedis && range !== "today" && dailyChecks.available,
  );

  const activitySource: DataSource =
    range === "today" ? circuitLiveSource(activity?.backend ?? cb?.backend) : hasActivityRedis ? "redis" : "memory";

  const activityHint =
    range === "today"
      ? activity?.backend === "redis"
        ? "Fleet total · UTC day"
        : "Since process start"
      : `Summed UTC days · ${rangeLabel(range)}`;

  const recentEvents = useMemo(() => {
    if (range === "today") return allRecentEvents;
    const cutoff = rangeStartUnix(range);
    return allRecentEvents.filter((e) => e.time >= cutoff);
  }, [allRecentEvents, range]);

  const refetchAll = () => {
    refetch();
    activityQuery.refetch();
  };

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
            <LiveIndicator
              updatedAt={Math.max(dataUpdatedAt, activityQuery.dataUpdatedAt)}
              fetching={isFetching || activityQuery.isFetching}
              onRefresh={refetchAll}
            />
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
          source={circuitLiveSource(cb?.backend)}
          valueClassName="text-lg"
        />
        <LiveStat
          title="Total failures"
          value={totalFailures}
          hint={failureStatHint}
          source={failureStatSource}
        />
      </div>

      {activity?.available ? (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
          <LiveStat title="State checks" value={checksPick.value} hint={activityHint} source={activitySource} />
          <LiveStat title="Blocked (open)" value={blockedPick.value} source={activitySource} />
          <LiveStat
            title="Recovery probes"
            value={probesPick.value}
            hint={range === "today" ? "After cooldown" : undefined}
            source={activitySource}
          />
          <LiveStat
            title="Probes succeeded"
            value={probesOkPick.value}
            hint={range === "today" ? "Circuit closed" : undefined}
            source={activitySource}
            valueClassName="text-success"
          />
          <LiveStat
            title="Probes failed"
            value={probesFailPick.value}
            hint={range === "today" ? "Circuit re-opened" : undefined}
            source={activitySource}
            valueClassName="text-error"
          />
        </div>
      ) : null}

      <div className="grid gap-4 lg:grid-cols-2">
        <ChartCard
          title="Failure trend"
          subtitle={useDailyFailureChart ? DAILY_HISTORY_SUBTITLE : LIVE_TREND_CHART_SUBTITLE}
          source={trendChartSource(useDailyFailureChart)}
        >
          {useDailyFailureChart ? (
            <BarChart
              labels={dailyFailures.labels}
              values={dailyFailures.values}
              label="Daily peak failures"
              colors={dailyFailures.labels.map(() => chartPalette.error())}
            />
          ) : (
            <TrendChart points={failureHistoryTrend} label="Failures" color={chartPalette.error()} />
          )}
        </ChartCard>
        <ChartCard
          title="Failures by provider"
          subtitle={
            range === "today"
              ? "Current 120s window"
              : `Peak daily window · ${rangeLabel(range)} · ${DAILY_HISTORY_SUBTITLE}`
          }
          source={failureStatSource}
        >
          <BarChart
            labels={names}
            values={providerFailureValues}
            label="Failures"
            colors={names.map((n) => stateColor(providers[n].state ?? "closed"))}
            horizontal
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

      {activity?.available && useDailyActivityChart ? (
        <ChartCard
          title="State checks"
          subtitle={`Daily UTC totals · ${rangeLabel(range)} · ${DAILY_HISTORY_SUBTITLE}`}
          source="redis"
        >
          <BarChart
            labels={dailyChecks.labels}
            values={dailyChecks.values}
            label="Checks"
            colors={dailyChecks.labels.map(() => chartPalette.primary())}
          />
        </ChartCard>
      ) : null}

      <SectionPanel
        title="Providers"
        subtitle={
          range === "today"
            ? "Live trip state and current-window failure counts"
            : `Live trip state · peak daily failures · ${rangeLabel(range)}`
        }
        source={range === "today" ? circuitLiveSource(cb?.backend) : failureStatSource}
      >
        <div className="overflow-x-auto">
          <table className="table table-zebra">
            <thead>
              <tr>
                <th>Provider</th>
                <th>State</th>
                <th>{range === "today" ? "Failures" : "Peak failures"}</th>
                <th>Rollup</th>
                <th>Threshold</th>
              </tr>
            </thead>
            <tbody>
              {names.map((name) => {
                const p = providers[name];
                const state = p.state ?? p.error ?? "unknown";
                const failures =
                  range === "today" || !hasFailureRedis
                    ? (p.failures ?? "—")
                    : (providerFailurePeaks.get(name) ?? 0);
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
                    <td>{failures}</td>
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

      {activity?.available ? (
        <SectionPanel
          title="Recovery activity"
          subtitle={
            range === "today"
              ? "Half-open probes after cooldown windows end (shared when Redis-backed)"
              : `Events since ${rangeLabel(range)} · ring buffer may truncate older entries`
          }
          source={activitySource}
        >
          <div className="overflow-x-auto">
            <table className="table table-zebra table-sm">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Event</th>
                  <th>Provider</th>
                  <th>Key</th>
                  <th>Result</th>
                  <th>Detail</th>
                </tr>
              </thead>
              <tbody>
                {recentEvents.map((e: CircuitActivityEvent, i) => (
                  <tr key={`${e.time}-${e.kind}-${i}`}>
                    <td className="whitespace-nowrap text-xs">{formatEventTime(e.time)}</td>
                    <td>{eventLabel(e.kind)}</td>
                    <td>
                      <ProviderBadge provider={e.provider} />
                    </td>
                    <td className="font-mono text-xs">{e.key ?? "—"}</td>
                    <td>
                      {e.new_state ? (
                        <StatusBadge
                          active={e.new_state === "closed"}
                          activeLabel="closed"
                          inactiveLabel={e.new_state}
                        />
                      ) : (
                        "—"
                      )}
                    </td>
                    <td className="text-xs text-base-content/70">
                      {e.status_code ? `HTTP ${e.status_code}` : null}
                      {e.failure_kind ? ` ${e.failure_kind}` : null}
                      {e.reason ? e.reason : null}
                    </td>
                  </tr>
                ))}
                {recentEvents.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="text-center text-base-content/50">
                      No recovery probes in this window — events appear when a cooldown ends and the
                      breaker probes upstream, or when requests are blocked while open.
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </SectionPanel>
      ) : null}
    </div>
  );
}
