import { useMemo } from "react";
import { Link } from "react-router-dom";

import { BarChart, ChartCard, TrendChart } from "../components/charts";
import { chartPalette } from "../components/charts/chart-setup";
import { FeatureFlagList } from "../components/ui/feature-flag-list";
import { circuitLiveSource, LiveStat, trendChartSource } from "../components/ui/data-source";
import PageHeader, {
  ErrorAlert,
  LiveIndicator,
  LoadingBlock,
  StatusBadge,
} from "../components/ui/page-header";
import { useConfig, useHealth, useKeys, useUsage } from "../hooks/queries";
import { LIVE_TREND_CHART_SUBTITLE, useHistory } from "../hooks/use-history";
import { aggScopeMap, DAILY_HISTORY_SUBTITLE, dailyHistoryChart, pickToday } from "../lib/daily-history";
import { featureEnabled } from "../lib/features";
import { compact, scopeKind, scopeLabel } from "../lib/format";

export default function OverviewPage() {
  const health = useHealth();
  const config = useConfig();
  const usage = useUsage();
  const keys = useKeys();

  const cb = health.data?.circuit_breaker;
  const providers = cb?.providers ?? {};
  const totalFailures =
    cb?.total_failures ?? Object.values(providers).reduce((sum, p) => sum + (p.failures ?? 0), 0);
  const failureHistory = useHistory(health.data ? totalFailures : undefined);
  const dailyFailures = useMemo(
    () => dailyHistoryChart(cb?.daily_history, "total_failures"),
    [cb?.daily_history],
  );
  const useDailyChart = Boolean(cb?.daily_history_available && dailyFailures.available);
  const circuitLive = circuitLiveSource(cb?.backend);

  const enabledFeatures = config.data?.features
    ? Object.values(config.data.features).filter((f) => featureEnabled(f)).length
    : 0;
  const totalFeatures = config.data?.features ? Object.keys(config.data.features).length : 0;

  const usageStats = usage.data?.stats;
  const usageHistory = usageStats?.daily_history;
  const usageRedis = Boolean(usageStats?.daily_history_available);
  const usageCounters = usageStats?.counters ?? {};
  const globalUsage = usageCounters["global"];
  // Prefer the fleet-wide today totals the backend merges from Redis over this
  // process's in-memory counters, so the card matches the Usage page (and isn't
  // just whatever traffic this one pod has served since its last restart).
  const requestsTodayPick = pickToday(
    usageStats?.available ? (usageStats?.requests_today ?? globalUsage?.requests) : undefined,
    usageHistory,
    "requests_today",
    usageRedis,
  );
  const tokensTodayPick = pickToday(
    usageStats?.available ? (usageStats?.tokens_today ?? globalUsage?.tokens) : undefined,
    usageHistory,
    "tokens_today",
    usageRedis,
  );
  const modelRows = (
    usageRedis
      ? aggScopeMap(usageHistory, "today", "by_model").map((s) => ({
          label: scopeLabel(s.scope),
          requests: s.requests,
        }))
      : Object.entries(usageCounters)
          .filter(([scope]) => scopeKind(scope) === "model")
          .map(([scope, c]) => ({ label: scopeLabel(scope), requests: c.requests ?? 0 }))
  )
    .sort((a, b) => b.requests - a.requests)
    .slice(0, 6);
  const modelSource = usageRedis ? "redis" : "memory";

  const isLoading = health.isLoading || config.isLoading || usage.isLoading;
  const error = health.error || config.error || usage.error;

  if (isLoading) return <LoadingBlock />;
  if (error) {
    return <ErrorAlert message={error instanceof Error ? error.message : "Failed to load overview"} />;
  }

  const providerNames = Object.keys(providers);
  const providerFailures = providerNames.map((n) => providers[n].failures ?? 0);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Overview"
        description="Live snapshot of proxy health, traffic, and configuration."
        actions={
          <LiveIndicator
            updatedAt={health.dataUpdatedAt}
            fetching={health.isFetching}
            onRefresh={() => {
              health.refetch();
              usage.refetch();
            }}
          />
        }
      />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <LiveStat
          title="Status"
          value={
            <StatusBadge
              active={health.data?.status === "healthy"}
              activeLabel="Healthy"
              inactiveLabel="Degraded"
            />
          }
          hint={health.data ? new Date(health.data.timestamp * 1000).toLocaleTimeString() : undefined}
          source="memory"
        />
        <LiveStat
          title="Requests today"
          value={compact(requestsTodayPick.value)}
          hint={`${modelRows.length} active models`}
          source={requestsTodayPick.source}
        />
        <LiveStat
          title="Tokens today"
          value={compact(tokensTodayPick.value)}
          hint="across all providers"
          source={tokensTodayPick.source}
        />
        <LiveStat
          title="Open circuits"
          value={Object.values(providers).filter((p) => p.rollup?.open || p.state === "open").length}
          hint={`${providerNames.length} providers · ${enabledFeatures}/${totalFeatures} features on · ${keys.data?.length ?? "—"} keys`}
          source={circuitLive}
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <ChartCard
            title="Circuit breaker failures"
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
        </div>
        <ChartCard title="Feature flags" subtitle="Current proxy capabilities" source="config">
          <FeatureFlagList features={config.data?.features} />
        </ChartCard>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <ChartCard
          title="Failures by provider"
          subtitle="Current failure count"
          source={circuitLive}
          actions={
            <Link to="/circuit" className="btn btn-ghost btn-xs">
              Details
            </Link>
          }
        >
          <BarChart
            labels={providerNames}
            values={providerFailures}
            label="Failures"
            colors={providerNames.map(() => chartPalette.warning())}
          />
        </ChartCard>
        <ChartCard
          title="Requests by model"
          subtitle="Top models today"
          source={modelSource}
          actions={
            <Link to="/usage" className="btn btn-ghost btn-xs">
              Usage
            </Link>
          }
        >
          <BarChart
            labels={modelRows.map((r) => r.label)}
            values={modelRows.map((r) => r.requests)}
            label="Requests"
            horizontal
          />
        </ChartCard>
      </div>
    </div>
  );
}
