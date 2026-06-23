import { useMemo, useState } from "react";

import { BarChart, ChartCard, DonutChart, TrendChart } from "../components/charts";
import { chartPalette } from "../components/charts/chart-setup";
import { TopKeysTable, RecentDetectionsTable } from "../components/tables/misc-tables";
import {
  type DataSource,
  LiveStat,
  RangeToggle,
  SectionPanel,
  trendChartSource,
} from "../components/ui/data-source";
import PageHeader, { ErrorAlert, LiveIndicator, LoadingBlock } from "../components/ui/page-header";
import { useKeys, usePII } from "../hooks/queries";
import { useByoBanActions } from "../hooks/use-byo-ban-actions";
import { LIVE_TREND_CHART_SUBTITLE, useHistory } from "../hooks/use-history";
import {
  DAILY_HISTORY_SUBTITLE,
  type NameCount,
  type RangeKey,
  RANGE_OPTIONS,
  aggNameCount,
  pickToday,
  scalarSeries,
} from "../lib/daily-history";
import type { PIINameCount } from "../types";

function rangeLabel(range: RangeKey): string {
  return range === "today" ? "today" : `last ${range === "7d" ? "7" : "30"} days`;
}

const ENTITY_COLORS = [
  chartPalette.primary,
  chartPalette.info,
  chartPalette.warning,
  chartPalette.success,
  chartPalette.error,
];

function toNameCount(rows: PIINameCount[]): NameCount[] {
  return rows.map((r) => ({ name: r.name, count: r.count }));
}

export default function PIIPage() {
  const { data, isLoading, error, dataUpdatedAt, isFetching, refetch } = usePII();
  const keys = useKeys();
  const byoBanActions = useByoBanActions();
  const [range, setRange] = useState<RangeKey>("today");

  const stats = data?.stats;
  const history = stats?.daily_history;
  const hasRedis = Boolean(stats?.daily_history_available);

  const detectedPick = pickToday(
    stats?.available ? stats?.requests_with_pii : undefined,
    history,
    "requests_with_pii",
    hasRedis,
  );
  const scannedPick = pickToday(
    stats?.available ? stats?.requests_scanned : undefined,
    history,
    "requests_scanned",
    hasRedis,
  );
  const entitiesPick = pickToday(
    stats?.available ? stats?.entities_total : undefined,
    history,
    "entities_total",
    hasRedis,
  );
  const detected = detectedPick.value;

  const detectionHistory = useHistory(stats?.available ? detected : undefined);
  const dailyDetections = useMemo(
    () => scalarSeries(history, "requests_with_pii", range),
    [history, range],
  );
  const useDailyChart = Boolean(hasRedis && range !== "today" && dailyDetections.available);

  if (isLoading) return <LoadingBlock />;
  if (error) {
    return <ErrorAlert message={error instanceof Error ? error.message : "Failed to load PII stats"} />;
  }
  if (!data) return null;

  const scanned = scannedPick.value;
  const rate = stats?.detection_rate ?? 0;
  const recent = stats?.recent ?? [];
  const recentSource: DataSource = stats?.recent_backend === "redis" ? "redis" : "memory";
  const failures = (stats?.fail_open ?? 0) + (stats?.fail_closed ?? 0);

  // Prefer fleet-wide Redis rollups whenever Redis is available — including
  // "today" — since in-process memory only reflects this one pod and undercounts
  // on a multi-pod fleet. Fall back to memory only when Redis isn't wired up.
  const memEntity = toNameCount(stats?.by_entity ?? []);
  const useRedisBreakdown = hasRedis || range !== "today";
  const breakdownSource: DataSource = useRedisBreakdown ? "redis" : "memory";
  const byEntity = useRedisBreakdown ? aggNameCount(history, range, "by_entity") : memEntity;
  const byProvider = useRedisBreakdown
    ? aggNameCount(history, range, "by_provider")
    : toNameCount(stats?.by_provider ?? []);
  const topKeys = useRedisBreakdown
    ? aggNameCount(history, range, "top_keys")
    : toNameCount(stats?.top_keys ?? []);
  const entitiesRangeTotal = byEntity.reduce((sum, e) => sum + e.count, 0);

  return (
    <div className="space-y-6">
      <PageHeader
        title="PII Redaction"
        description="Live detection stats from the Presidio redaction pipeline. Metadata only — no raw PII is stored."
        actions={
          <div className="flex items-center gap-3">
            <RangeToggle value={range} options={RANGE_OPTIONS} onChange={setRange} />
            <LiveIndicator updatedAt={dataUpdatedAt} fetching={isFetching} onRefresh={() => refetch()} />
          </div>
        }
      />

      {!data.enabled ? (
        <div className="alert">
          <span>
            PII redaction is globally disabled
            {data.allow_per_key_override ? " (per-key override allowed)." : "."}
          </span>
        </div>
      ) : null}

      {!stats?.available ? (
        <div className="alert alert-info">
          <span>
            Stats collection is inactive — the redaction middleware is not installed. Enable
            <code className="mx-1">features.pii_redact</code>to begin recording detections.
          </span>
        </div>
      ) : null}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <LiveStat
          title="Fail mode"
          value={<span className="capitalize">{data.fail_mode}</span>}
          hint={data.allow_per_key_override ? "per-key override on" : "global only"}
          source="config"
        />
        <LiveStat
          title="Requests scanned"
          value={scanned.toLocaleString()}
          hint="today UTC"
          source={scannedPick.source}
        />
        <LiveStat
          title="With PII"
          value={detected.toLocaleString()}
          hint={`${(rate * 100).toFixed(1)}% detection rate`}
          source={detectedPick.source}
        />
        <LiveStat
          title="Entities redacted"
          value={entitiesPick.value.toLocaleString()}
          hint={`${failures} failures · ${stats?.oversize ?? 0} oversize`}
          source={entitiesPick.source}
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <ChartCard
            title="PII detections over time"
            subtitle={useDailyChart ? DAILY_HISTORY_SUBTITLE : LIVE_TREND_CHART_SUBTITLE}
            source={trendChartSource(useDailyChart)}
          >
            {useDailyChart ? (
              <BarChart
                labels={dailyDetections.labels}
                values={dailyDetections.values}
                label="Requests with PII"
                colors={dailyDetections.labels.map(() => chartPalette.warning())}
              />
            ) : (
              <TrendChart points={detectionHistory} label="Requests with PII" color={chartPalette.warning()} />
            )}
          </ChartCard>
        </div>
        <ChartCard
          title="Entity types"
          subtitle={`Share of redacted entities · ${rangeLabel(range)}`}
          source={breakdownSource}
        >
          <DonutChart
            labels={byEntity.map((e) => e.name.replaceAll("_", " "))}
            values={byEntity.map((e) => e.count)}
            colors={byEntity.map((_, i) => ENTITY_COLORS[i % ENTITY_COLORS.length]())}
            centerValue={entitiesRangeTotal.toLocaleString()}
            centerLabel="entities"
          />
        </ChartCard>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <ChartCard
          title="Top entity types"
          subtitle={`Count by Presidio entity · ${rangeLabel(range)}`}
          source={breakdownSource}
        >
          <BarChart
            labels={byEntity.map((e) => e.name.replaceAll("_", " "))}
            values={byEntity.map((e) => e.count)}
            label="Detections"
            colors={byEntity.map(() => chartPalette.primary())}
            horizontal
          />
        </ChartCard>
        <ChartCard
          title="By provider"
          subtitle={`Scanned requests per provider · ${rangeLabel(range)}`}
          source={breakdownSource}
        >
          <BarChart
            labels={byProvider.map((p) => p.name)}
            values={byProvider.map((p) => p.count)}
            label="Requests"
            colors={byProvider.map(() => chartPalette.info())}
          />
        </ChartCard>
      </div>

      {topKeys.length > 0 ? (
        <SectionPanel
          title="Top keys"
          subtitle={
            useRedisBreakdown
              ? `Summed Redis rollups · ${rangeLabel(range)}`
              : "Top 10 by detection count (today, live)"
          }
          source={breakdownSource}
        >
          <TopKeysTable rows={topKeys} keys={keys.data ?? []} byoBanActions={byoBanActions} />
        </SectionPanel>
      ) : null}

      <SectionPanel
        title="Recent scans"
        subtitle={
          recentSource === "redis"
            ? `Last ${recent.length} redaction scans fleet-wide (clean, detected, and failed)`
            : `Last ${recent.length} redaction scans on this pod (clean, detected, and failed)`
        }
        source={recentSource}
      >
        <RecentDetectionsTable rows={recent} keys={keys.data ?? []} byoBanActions={byoBanActions} />
      </SectionPanel>
    </div>
  );
}
