import { Link, useParams } from "react-router-dom";

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
import { useCost, useKey, usePII, useRateLimits } from "../../hooks/queries";
import { aggCostByKey, DAILY_HISTORY_SUBTITLE, costSeriesForKey } from "../../lib/daily-history";
import { formatCount, formatDailyCostLimit, formatUsd, maskKeyId } from "../../lib/format";
import { decodeKeyRouteParam, isProxyKey } from "../../lib/key-routes";
import {
  costRecentForKey,
  costStatsForKey,
  piiDetectionsForKey,
  piiRecentForKey,
  rateLimitOverrideForKey,
  rateLimitUsageForKey,
} from "../../lib/key-stats";
import type { PIIRecentEvent } from "../../types";

function piiOutcomeBadge(outcome: PIIRecentEvent["outcome"]) {
  const map: Record<PIIRecentEvent["outcome"], string> = {
    ok: "badge-success",
    fail_open: "badge-warning",
    fail_closed: "badge-error",
    oversize: "badge-ghost",
  };
  return <span className={`badge badge-sm ${map[outcome]}`}>{outcome}</span>;
}

function piiLabel(value: boolean | null | undefined): string {
  if (value === true) return "On";
  if (value === false) return "Off";
  return "Inherit";
}

function formatLimit(value?: number): string {
  return value && value > 0 ? value.toLocaleString() : "∞";
}

export default function KeyDetailPage() {
  const { key: keyParam } = useParams<{ key: string }>();
  const keyValue = keyParam ? decodeKeyRouteParam(keyParam) : undefined;
  const validKey = isProxyKey(keyValue) ? keyValue : undefined;

  const keyQuery = useKey(validKey);
  const costQuery = useCost();
  const piiQuery = usePII();
  const rateQuery = useRateLimits();

  const keyRecord = keyQuery.data;
  const keyError = keyQuery.error;

  const masked = validKey ? maskKeyId(validKey) : "";
  const costStats = costStatsForKey(costQuery.data?.stats, validKey ?? "");
  const costRecent = costRecentForKey(costQuery.data?.stats, validKey ?? "");
  const piiRecent = piiRecentForKey(piiQuery.data?.stats, validKey ?? "");
  const piiDetections = piiDetectionsForKey(piiQuery.data?.stats, validKey ?? "");
  const rateUsage = rateLimitUsageForKey(rateQuery.data, validKey ?? "");
  const rateOverride = rateLimitOverrideForKey(rateQuery.data, validKey ?? "");
  const rateSource = rateLimitUsageSource(rateQuery.data?.backend);
  const costHistory = costQuery.data?.stats?.daily_history;
  const hasCostRedis = Boolean(costQuery.data?.stats?.daily_history_available);
  const keySpend7d = costSeriesForKey(costHistory, masked, "7d");
  // Prefer the fleet-wide Redis today rollup for this key over this pod's memory.
  const keyTodayCost = hasCostRedis
    ? aggCostByKey(costHistory, "today").find((r) => r.key_id === masked)
    : undefined;
  const spendTodayValue = keyTodayCost ? keyTodayCost.spend_usd : (costStats?.spend_usd ?? 0);
  const requestsTodayValue = keyTodayCost ? keyTodayCost.requests : (costStats?.requests ?? 0);
  const keyCostSource: DataSource = keyTodayCost ? "redislive" : "memory";
  // PII detections come from the fleet-wide top_keys rollup (overlaid by
  // MergeToday) when Redis is on; only falls back to the memory ring buffer for
  // keys outside the rolled-up top-N.
  const hasPiiRedis = Boolean(piiQuery.data?.stats?.daily_history_available);
  const piiSource: DataSource = hasPiiRedis ? "redislive" : "memory";

  const liveUpdatedAt = Math.max(
    costQuery.dataUpdatedAt,
    piiQuery.dataUpdatedAt,
    rateQuery.dataUpdatedAt,
    keyQuery.dataUpdatedAt,
  );
  const liveFetching =
    costQuery.isFetching || piiQuery.isFetching || rateQuery.isFetching || keyQuery.isFetching;

  const refreshAll = () => {
    keyQuery.refetch();
    costQuery.refetch();
    piiQuery.refetch();
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

  if (keyQuery.isLoading && !costQuery.data && !piiQuery.data && !rateQuery.data) {
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
            ? "This key is not registered (it may have been deleted). Live stats below are matched by masked key id."
            : keyRecord?.description
              ? masked
              : "Per-key cost, PII, and rate-limit stats."
        }
        actions={<LiveIndicator updatedAt={liveUpdatedAt} fetching={liveFetching} onRefresh={refreshAll} />}
      />

      {notFound ? (
        <div className="alert alert-warning">
          <span>Key metadata unavailable — showing stats for {masked} only.</span>
        </div>
      ) : null}

      {keyRecord ? (
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
                <span className="badge badge-ghost badge-sm">PII {piiLabel(keyRecord.redact_pii)}</span>
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
              <Meta label="Created" value={new Date(keyRecord.created_at).toLocaleString()} />
              <Meta label="Updated" value={new Date(keyRecord.updated_at).toLocaleString()} />
            </div>
          </div>
        </div>
      ) : null}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <LiveStat title="Spend today" value={formatUsd(spendTodayValue)} hint="tracked cost" source={keyCostSource} />
        <LiveStat
          title="Requests"
          value={requestsTodayValue.toLocaleString()}
          hint="cost tracker"
          source={keyCostSource}
        />
        <LiveStat
          title="PII detections"
          value={piiDetections.toLocaleString()}
          hint={`${piiRecent.length} recent events`}
          source={piiSource}
        />
        <LiveStat
          title="Rate usage"
          value={rateUsage.reduce((s, r) => s + r.requests, 0).toLocaleString()}
          hint="requests in live windows"
          source={rateSource}
        />
      </div>

      {hasCostRedis && keySpend7d.available ? (
        <ChartCard
          title="Spend over time"
          subtitle={`Last 7 days for this key · ${DAILY_HISTORY_SUBTITLE}`}
          source="redis"
        >
          <BarChart
            labels={keySpend7d.labels}
            values={keySpend7d.values}
            label="Daily spend (USD)"
            colors={keySpend7d.labels.map(() => chartPalette.primary())}
          />
        </ChartCard>
      ) : null}

      <div className="grid gap-4 lg:grid-cols-2">
        <Section title="Cost" subtitle="Today's tracked spend for this key" source="memory">
          <div className="grid gap-4 p-5 sm:grid-cols-2 lg:grid-cols-3">
            <Meta label="Total spend" value={formatUsd(costStats?.spend_usd ?? 0)} />
            <Meta label="Input spend" value={formatUsd(costStats?.input_spend_usd ?? 0)} />
            <Meta label="Output spend" value={formatUsd(costStats?.output_spend_usd ?? 0)} />
            <Meta label="Requests" value={costStats?.requests ?? 0} />
            <Meta label="Input tokens" value={(costStats?.input_tokens ?? 0).toLocaleString()} />
            <Meta label="Output tokens" value={(costStats?.output_tokens ?? 0).toLocaleString()} />
          </div>
          <EventsTable
            empty="No cost events for this key yet"
            headers={["Time", "Provider", "Model", "Total", "Input", "Output", "Tokens"]}
            rows={costRecent.map((ev, i) => (
              <tr key={`${ev.time}-${ev.model}-${i}`}>
                <td className="whitespace-nowrap text-base-content/70">
                  {new Date(ev.time * 1000).toLocaleTimeString()}
                </td>
                <td>
                  <ProviderBadge provider={ev.provider} />
                </td>
                <td className="text-xs text-base-content/60">{ev.model ?? "—"}</td>
                <td>{formatUsd(ev.spend_usd)}</td>
                <td>{formatUsd(ev.input_spend_usd ?? 0)}</td>
                <td>{formatUsd(ev.output_spend_usd ?? 0)}</td>
                <td className="text-base-content/70">
                  {ev.input_tokens}/{ev.output_tokens}
                </td>
              </tr>
            ))}
          />
        </Section>

        <Section title="PII redaction" subtitle="Recent events are memory-only (last 50)" source="memory">
          <div className="grid gap-4 p-5 sm:grid-cols-2">
            <Meta label="Recent events" value={piiRecent.length} />
            <Meta label="Top-key count" value={piiDetections} />
            <Meta label="Global fail mode" value={piiQuery.data?.fail_mode ?? "—"} />
            <Meta label="Per-key override" value={keyRecord ? piiLabel(keyRecord.redact_pii) : "—"} />
          </div>
          <EventsTable
            empty="No PII events for this key yet"
            headers={["Time", "Provider", "Entities", "Outcome", "Latency"]}
            rows={piiRecent.map((ev, i) => (
              <tr key={`${ev.time}-${i}`}>
                <td className="whitespace-nowrap text-base-content/70">
                  {new Date(ev.time * 1000).toLocaleTimeString()}
                </td>
                <td>
                  <ProviderBadge provider={ev.provider} />
                </td>
                <td>
                  {ev.entity_total > 0 ? (
                    <div className="flex flex-wrap gap-1">
                      {Object.entries(ev.entity_counts).map(([name, n]) => (
                        <span key={name} className="badge badge-sm badge-outline">
                          {name.replaceAll("_", " ")} ×{n}
                        </span>
                      ))}
                    </div>
                  ) : (
                    <span className="text-base-content/40">clean</span>
                  )}
                </td>
                <td>{piiOutcomeBadge(ev.outcome)}</td>
                <td className="text-base-content/70">{ev.duration_ms.toFixed(1)} ms</td>
              </tr>
            ))}
          />
        </Section>
      </div>

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
        <EventsTable
          empty="No rate-limit usage recorded for this key"
          headers={["Window", "Requests", "Tokens"]}
          rows={rateUsage.map((row) => (
            <tr key={row.window}>
              <td className="capitalize">{row.window === "day" ? "Today" : "Last minute"}</td>
              <td>{formatCount(row.requests)}</td>
              <td>{formatCount(row.tokens)}</td>
            </tr>
          ))}
        />
      </Section>
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

function EventsTable({
  headers,
  rows,
  empty,
}: {
  headers: string[];
  rows: React.ReactNode[];
  empty: string;
}) {
  return (
    <div className="overflow-x-auto border-t border-base-300/70">
      <table className="table table-zebra">
        <thead>
          <tr>
            {headers.map((h) => (
              <th key={h}>{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length > 0 ? (
            rows
          ) : (
            <tr>
              <td colSpan={headers.length} className="text-center text-base-content/50">
                {empty}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
