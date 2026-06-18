import { Link, useParams } from "react-router-dom";

import { KeyCostEventsTable, KeyPiiEventsTable, KeyRateUsageTable } from "../../components/keys/key-detail-tables";
import { CopyButton } from "../../components/ui/copy-button";
import { MaskedKey } from "../../components/ui/masked-key";
import PageHeader, {
  ErrorAlert,
  LiveIndicator,
  LoadingBlock,
  ProviderBadge,
  StatusBadge,
} from "../../components/ui/page-header";
import {
  DataSourceBadge,
  LiveStat,
  rateLimitUsageSource,
  type DataSource,
} from "../../components/ui/data-source";
import { BarChart, ChartCard } from "../../components/charts";
import { chartPalette } from "../../components/charts/chart-setup";
import { useKey, useKeyStats, useMe, usePII, useRateLimits } from "../../hooks/queries";
import { DAILY_HISTORY_SUBTITLE } from "../../lib/daily-history";
import {
  formatDailyCostLimit,
  formatMonthlyCostLimit,
  formatUsd,
  isPersonalKey,
  maskKeyId,
} from "../../lib/format";
import { decodeKeyRouteParam, isProxyKey } from "../../lib/key-routes";
import { rateLimitOverrideForKey, rateLimitUsageForKey } from "../../lib/key-stats";
import type { KeyStatsSource } from "../../types";

function piiLabel(value: boolean | null | undefined): string {
  if (value === true) return "On";
  if (value === false) return "Off";
  return "Inherit";
}

function formatLimit(value?: number): string {
  return value && value > 0 ? value.toLocaleString() : "∞";
}

function statsSource(source: KeyStatsSource): DataSource {
  return source;
}

function chartLabels(points: { day: string }[]): string[] {
  return points.map((p) => p.day.slice(5));
}

export default function KeyDetailPage() {
  const { key: keyParam } = useParams<{ key: string }>();
  const { data: me } = useMe();
  const isViewer = me?.role === "viewer";
  const keyValue = keyParam ? decodeKeyRouteParam(keyParam) : undefined;
  const validKey = isProxyKey(keyValue) ? keyValue : undefined;

  const keyQuery = useKey(validKey);
  const statsQuery = useKeyStats(validKey);
  const piiQuery = usePII();
  const rateQuery = useRateLimits();

  const keyRecord = keyQuery.data;
  const keyError = keyQuery.error;
  const stats = statsQuery.data;

  const masked = validKey ? maskKeyId(validKey) : "";
  const rateUsage = rateLimitUsageForKey(rateQuery.data, validKey ?? "");
  const rateOverride = rateLimitOverrideForKey(rateQuery.data, validKey ?? "");
  const rateSource = rateLimitUsageSource(rateQuery.data?.backend);

  const costToday = stats?.cost_today;
  const piiToday = stats?.pii_today;
  const costSource = statsSource(costToday?.source ?? "memory");
  const piiSource = statsSource(piiToday?.source ?? "memory");
  const costHistory = stats?.cost_history ?? [];
  const piiHistory = stats?.pii_history ?? [];
  const recentCost = stats?.recent_cost ?? [];
  const recentPii = stats?.recent_pii ?? [];

  const liveUpdatedAt = Math.max(
    statsQuery.dataUpdatedAt,
    rateQuery.dataUpdatedAt,
    keyQuery.dataUpdatedAt,
  );
  const liveFetching = statsQuery.isFetching || rateQuery.isFetching || keyQuery.isFetching;

  const refreshAll = () => {
    keyQuery.refetch();
    statsQuery.refetch();
    rateQuery.refetch();
  };

  if (!keyValue) {
    return <ErrorAlert message="Missing key in URL." />;
  }

  if (!validKey) {
    return (
      <div className="space-y-4">
        <Link to="/keys" className="link link-hover text-sm text-base-content/60">
          ← API Keys
        </Link>
        <ErrorAlert message="Invalid key link — open this page from a registered iw: proxy key." />
      </div>
    );
  }

  if (keyQuery.isPending || statsQuery.isPending || (!isViewer && rateQuery.isPending)) {
    return <LoadingBlock />;
  }

  const title = keyRecord?.description || masked;
  const notFound = Boolean(keyError && !keyRecord);

  return (
    <div className="space-y-6">
      <div className="text-sm">
        <Link to="/keys" className="link link-hover text-base-content/60">
          ← API Keys
        </Link>
      </div>

      <PageHeader
        title={title}
        description={
          notFound
            ? "This key is not registered (it may have been deleted)."
            : isViewer
              ? "Your personal proxy key."
              : keyRecord?.description
                ? masked
                : "Per-key cost, PII, and rate-limit stats."
        }
        actions={
          <LiveIndicator updatedAt={liveUpdatedAt} fetching={liveFetching} onRefresh={refreshAll} />
        }
      />

      {notFound ? (
        <div className="alert alert-warning">
          <span>Key metadata unavailable — open this page from a registered key to see stats.</span>
        </div>
      ) : null}

      {keyRecord && !notFound ? (
        <div className="glass-panel p-5">
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium text-base-content/70">Key metadata</span>
            <DataSourceBadge source="dynamodb" />
          </div>
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div className="space-y-3">
              <div className="flex flex-wrap items-center gap-2">
                <ProviderBadge provider={keyRecord.provider} />
                <StatusBadge active={keyRecord.enabled} activeLabel="Enabled" inactiveLabel="Disabled" />
                {!isViewer ? (
                  <span className="badge badge-ghost badge-sm">PII {piiLabel(keyRecord.redact_pii)}</span>
                ) : null}
              </div>
              <div className="flex items-center gap-2">
                <MaskedKey value={keyRecord.key} />
                <CopyButton value={keyRecord.key} label="Copy key" />
              </div>
              {keyRecord.description ? (
                <p className="text-sm text-base-content/70">{keyRecord.description}</p>
              ) : null}
            </div>
            <div className="grid gap-3 text-sm sm:grid-cols-2">
              {isViewer || isPersonalKey(keyRecord) ? (
                <Meta label="Monthly cost limit" value={formatMonthlyCostLimit(keyRecord.monthly_cost_limit)} />
              ) : (
                <>
                  <Meta label="Daily cost limit" value={formatDailyCostLimit(keyRecord.daily_cost_limit)} />
                  <Meta
                    label="Rate limits"
                    value={
                      [
                        keyRecord.rate_limit_rpm ? `${keyRecord.rate_limit_rpm} rpm` : null,
                        keyRecord.rate_limit_tpm ? `${keyRecord.rate_limit_tpm.toLocaleString()} tpm` : null,
                      ]
                        .filter(Boolean)
                        .join(" · ") || "—"
                    }
                  />
                </>
              )}
              <Meta label="Created" value={new Date(keyRecord.created_at).toLocaleString()} />
              <Meta label="Updated" value={new Date(keyRecord.updated_at).toLocaleString()} />
            </div>
          </div>
        </div>
      ) : null}

      {keyRecord && !notFound ? (
        <>
          <div className={`grid gap-4 sm:grid-cols-2 ${isViewer ? "xl:grid-cols-3" : "xl:grid-cols-4"}`}>
            <LiveStat
              title="Spend today"
              value={formatUsd(costToday?.spend_usd ?? 0)}
              hint="tracked cost"
              source={costSource}
            />
            <LiveStat
              title="Requests"
              value={(costToday?.requests ?? 0).toLocaleString()}
              hint="cost tracker"
              source={costSource}
            />
            <LiveStat
              title="PII detections"
              value={(piiToday?.detections ?? 0).toLocaleString()}
              hint={`${recentPii.length} recent events`}
              source={piiSource}
            />
            {!isViewer ? (
              <LiveStat
                title="Rate usage"
                value={rateUsage.reduce((s, r) => s + r.requests, 0).toLocaleString()}
                hint="requests in live windows"
                source={rateSource}
              />
            ) : null}
          </div>

          {stats?.rollup_available && costHistory.length > 0 ? (
            <ChartCard
              title="Spend over time"
              subtitle={`Last 7 days for this key · ${DAILY_HISTORY_SUBTITLE}`}
              source="redis"
            >
              <BarChart
                labels={chartLabels(costHistory)}
                values={costHistory.map((p) => p.value)}
                label="Daily spend (USD)"
                colors={costHistory.map(() => chartPalette.primary())}
              />
            </ChartCard>
          ) : null}

          {stats?.rollup_available && piiHistory.length > 0 ? (
            <ChartCard
              title="PII detections over time"
              subtitle={`Last 7 days for this key · ${DAILY_HISTORY_SUBTITLE}`}
              source="redis"
            >
              <BarChart
                labels={chartLabels(piiHistory)}
                values={piiHistory.map((p) => p.value)}
                label="Daily detections"
                colors={piiHistory.map(() => chartPalette.warning())}
              />
            </ChartCard>
          ) : null}

          <div className="grid gap-4 lg:grid-cols-2">
            <Section
              title="Cost"
              subtitle={
                stats?.rollup_available
                  ? "Today's fleet-wide rollup for this key (direct Redis read) · recent events below are memory-only (last 50)"
                  : "Today's tracked spend for this key · recent events are memory-only (last 50)"
              }
              source={costSource}
            >
              <div className="grid gap-4 p-5 sm:grid-cols-2 lg:grid-cols-3">
                <Meta label="Total spend" value={formatUsd(costToday?.spend_usd ?? 0)} />
                <Meta label="Input spend" value={formatUsd(costToday?.input_spend_usd ?? 0)} />
                <Meta label="Output spend" value={formatUsd(costToday?.output_spend_usd ?? 0)} />
                <Meta label="Requests" value={costToday?.requests ?? 0} />
                <Meta label="Input tokens" value={(costToday?.input_tokens ?? 0).toLocaleString()} />
                <Meta label="Output tokens" value={(costToday?.output_tokens ?? 0).toLocaleString()} />
              </div>
              <KeyCostEventsTable rows={recentCost} />
            </Section>

            <Section
              title="PII redaction"
              subtitle={
                stats?.rollup_available
                  ? "Detection count from fleet-wide Redis (direct read) · recent events below are memory-only (last 50)"
                  : "Recent events are memory-only (last 50)"
              }
              source={piiSource}
            >
              <div className="grid gap-4 p-5 sm:grid-cols-2">
                <Meta label="Recent events" value={recentPii.length} />
                <Meta label="Top-key count" value={piiToday?.detections ?? 0} />
                {!isViewer ? (
                  <>
                    <Meta label="Global fail mode" value={piiQuery.data?.fail_mode ?? "—"} />
                    <Meta label="Per-key override" value={keyRecord ? piiLabel(keyRecord.redact_pii) : "—"} />
                  </>
                ) : null}
              </div>
              <KeyPiiEventsTable rows={recentPii} />
            </Section>
          </div>

          {!isViewer ? (
            <Section
              title="Rate limits"
              subtitle="Overrides from key config (DynamoDB); usage from rate-limit backend"
              source={rateSource}
            >
              <div className="grid gap-4 p-5 sm:grid-cols-2 lg:grid-cols-4">
                <Meta label="RPM override" value={formatLimit(rateOverride?.RequestsPerMinute ?? keyRecord?.rate_limit_rpm)} />
                <Meta label="TPM override" value={formatLimit(rateOverride?.TokensPerMinute ?? keyRecord?.rate_limit_tpm)} />
                <Meta label="RPD override" value={formatLimit(rateOverride?.RequestsPerDay ?? keyRecord?.rate_limit_rpd)} />
                <Meta label="TPD override" value={formatLimit(rateOverride?.TokensPerDay ?? keyRecord?.rate_limit_tpd)} />
              </div>
              <KeyRateUsageTable rows={rateUsage} />
            </Section>
          ) : null}
        </>
      ) : null}
    </div>
  );
}

function Meta({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div>
      <p className="text-xs uppercase tracking-wide text-base-content/50">{label}</p>
      <p className="font-medium">{value ?? "—"}</p>
    </div>
  );
}

function Section({
  title,
  subtitle,
  source,
  children,
}: {
  title: string;
  subtitle?: string;
  source?: DataSource;
  children: React.ReactNode;
}) {
  return (
    <div className="glass-panel overflow-hidden">
      <div className="border-b border-base-300/70 px-5 py-4">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="font-semibold">{title}</h3>
          {source ? <DataSourceBadge source={source} /> : null}
        </div>
        {subtitle ? <p className="mt-1 text-sm text-base-content/60">{subtitle}</p> : null}
      </div>
      {children}
    </div>
  );
}
