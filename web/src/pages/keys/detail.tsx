import { FormEvent, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import { KeyCostEventsTable, KeyPiiEventsTable, KeyRateUsageTable } from "../../components/keys/key-detail-tables";
import { ProxyKeyUsagePanel } from "../../components/keys/proxy-key-usage-panel";
import { CopyButton } from "../../components/ui/copy-button";
import { MaskedKey } from "../../components/ui/masked-key";
import { MaskedCredentialId } from "../../components/ui/masked-credential-id";
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
import { SpendOverview, SpendPeriodPanel } from "../../components/ui/spend-breakdown";
import { BarChart, ChartCard } from "../../components/charts";
import { chartPalette } from "../../components/charts/chart-setup";
import { useKey, useKeyStats, useMe, usePII, useRateLimits, useUpdateKey } from "../../hooks/queries";
import { DAILY_HISTORY_SUBTITLE } from "../../lib/daily-history";
import {
  formatDailyCostLimit,
  formatMonthlyCostLimit,
  formatMonthYear,
  formatUsd,
  effectiveDailyLimitCents,
  effectiveMonthlyLimitCents,
  isPersonalKey,
  maskKeyId,
} from "../../lib/format";
import { decodeKeyRouteParam, isKeyRouteParam, isMaskedKeyRouteParam, isProxyKey, keyDetailPath } from "../../lib/key-routes";
import { dismissKeySetup, isKeySetupDismissed } from "../../lib/key-setup-dismiss";
import { rateLimitOverrideForKey, rateLimitUsageForKey } from "../../lib/key-stats";
import type { KeyStatsSource } from "../../types";
import { useToast } from "../../components/ui/toast";

type DetailTab = "cost" | "pii" | "rate-limits" | "usage";

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

function detailTabClass(active: boolean): string {
  return active
    ? "btn btn-primary btn-sm gap-2 shadow-sm"
    : "btn btn-ghost btn-sm gap-2 text-base-content/70 hover:bg-base-200/70 hover:text-base-content";
}

export default function KeyDetailPage() {
  const navigate = useNavigate();
  const { push } = useToast();
  const { key: keyParam } = useParams<{ key: string }>();
  const { data: me } = useMe();
  const isViewer = me?.role === "viewer";
  const routeKeyId = keyParam ? decodeKeyRouteParam(keyParam) : undefined;
  const validRoute = isKeyRouteParam(routeKeyId) ? routeKeyId : undefined;

  const keyQuery = useKey(validRoute);
  const statsQuery = useKeyStats(validRoute);
  const updateKey = useUpdateKey();
  const piiQuery = usePII();
  const rateQuery = useRateLimits();
  const [tab, setTab] = useState<DetailTab>("cost");
  const tabDefaultedRef = useRef(false);
  const [editingName, setEditingName] = useState(false);
  const [nameDraft, setNameDraft] = useState("");

  const keyRecord = keyQuery.data;
  const proxyKey = keyRecord?.key;
  const [setupDismissed, setSetupDismissed] = useState(() =>
    proxyKey ? isKeySetupDismissed(proxyKey) : false,
  );

  useEffect(() => {
    if (!proxyKey) return;
    setSetupDismissed(isKeySetupDismissed(proxyKey));
  }, [proxyKey]);

  useEffect(() => {
    if (!routeKeyId || !isProxyKey(routeKeyId) || isMaskedKeyRouteParam(routeKeyId)) return;
    navigate(keyDetailPath(routeKeyId), { replace: true });
  }, [routeKeyId, navigate]);

  useEffect(() => {
    if (keyRecord?.description != null) {
      setNameDraft(keyRecord.description);
    }
  }, [keyRecord?.description]);

  const keyError = keyQuery.error;
  const stats = statsQuery.data;

  const masked = proxyKey ? maskKeyId(proxyKey) : routeKeyId ?? "";
  const rateUsageFromStats = stats?.rate_usage ?? [];
  const rateUsageLegacy = rateLimitUsageForKey(rateQuery.data, proxyKey ?? "");
  const rateUsage =
    rateUsageFromStats.length > 0
      ? rateUsageFromStats.map((row) => ({
        window: row.window,
        requests: row.requests,
        tokens: row.tokens,
      }))
      : rateUsageLegacy;
  const rateOverride = rateLimitOverrideForKey(rateQuery.data, proxyKey ?? "");
  const rateSource = rateLimitUsageSource(stats?.rate_backend ?? rateQuery.data?.backend);

  const costToday = stats?.cost_today;
  const costMonth = stats?.cost_month;
  const piiToday = stats?.pii_today;
  const costSource = statsSource(costToday?.source ?? "memory");
  const monthSource = statsSource(costMonth?.source ?? "memory");
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

  const requestsToday = costToday?.requests ?? 0;
  const statsLoaded = !statsQuery.isPending;

  const hasBeenUsed = Boolean(
    keyRecord?.first_request_at
    || (statsLoaded && (
      requestsToday > 0
      || (costMonth?.spend_usd ?? 0) > 0
      || recentCost.length > 0
    )),
  );

  const setupMode = Boolean(
    isViewer && keyRecord && !hasBeenUsed && !setupDismissed,
  );

  useEffect(() => {
    if (tabDefaultedRef.current || keyQuery.isPending || statsQuery.isPending || !validRoute) return;
    tabDefaultedRef.current = true;
    if (isViewer && !hasBeenUsed && !setupDismissed) {
      setTab("usage");
    }
  }, [hasBeenUsed, isViewer, keyQuery.isPending, setupDismissed, statsQuery.isPending, validRoute]);

  const dismissSetup = () => {
    if (!proxyKey) return;
    dismissKeySetup(proxyKey);
    setSetupDismissed(true);
  };

  const saveName = async (event: FormEvent) => {
    event.preventDefault();
    if (!proxyKey || !validRoute) return;
    try {
      await updateKey.mutateAsync({
        key: validRoute,
        body: { description: nameDraft.trim() },
      });
      push("Name updated", "success");
      setEditingName(false);
    } catch (err) {
      push(err instanceof Error ? err.message : "Failed to update name", "error");
    }
  };

  const sdkBaseUrl = keyRecord?.base_url;

  if (!routeKeyId) {
    return <ErrorAlert message="Missing key in URL." />;
  }

  if (!validRoute) {
    return (
      <div className="space-y-4">
        <Link to="/keys" className="link link-hover text-sm text-base-content/60">
          ← API Keys
        </Link>
        <ErrorAlert message="Invalid key link — open this page from a registered iw: proxy key." />
      </div>
    );
  }

  if (keyQuery.isPending || statsQuery.isPending || (!isViewer && rateQuery.isPending && !(stats?.rate_usage?.length))) {
    return <LoadingBlock />;
  }

  const title = keyRecord?.description?.trim() || "Unnamed key";
  const notFound = Boolean(keyError && !keyRecord);
  const isPersonal = keyRecord ? isPersonalKey(keyRecord) : false;
  const viewerMonthlyCents = me?.viewer_limits?.personal_monthly_cost_limit_cents ?? 0;
  const dailyLimitCents = keyRecord ? effectiveDailyLimitCents(keyRecord) : 0;
  const monthlyLimitCents = keyRecord
    ? effectiveMonthlyLimitCents(keyRecord, viewerMonthlyCents)
    : 0;
  const monthLabel = formatMonthYear(costMonth?.month);
  const rateRequestTotal = rateUsage.reduce((s, r) => s + r.requests, 0);

  return (
    <div className="space-y-6">
      <div className="text-sm">
        <Link to="/keys" className="link link-hover text-base-content/60">
          ← API Keys
        </Link>
      </div>

      <PageHeader
        title={
          editingName ? (
            <form className="flex flex-wrap items-center gap-2" onSubmit={saveName}>
              <input
                type="text"
                className="input input-bordered input-sm w-full max-w-md"
                value={nameDraft}
                onChange={(event) => setNameDraft(event.target.value)}
                placeholder="Key name"
                autoFocus
              />
              <button type="submit" className="btn btn-primary btn-sm" disabled={updateKey.isPending}>
                Save
              </button>
              <button
                type="button"
                className="btn btn-ghost btn-sm"
                onClick={() => {
                  setEditingName(false);
                  setNameDraft(keyRecord?.description ?? "");
                }}
              >
                Cancel
              </button>
            </form>
          ) : (
            <span className="inline-flex flex-wrap items-center gap-2">
              <span>{title}</span>
              {keyRecord && !notFound ? (
                <button
                  type="button"
                  className="btn btn-ghost btn-xs text-base-content/60"
                  onClick={() => setEditingName(true)}
                >
                  Rename
                </button>
              ) : null}
            </span>
          )
        }
        description={
          notFound
            ? "This key is not registered (it may have been deleted)."
            : isViewer
              ? "Your personal proxy key."
              : masked
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
            <span className="text-sm font-medium text-base-content/70">Key</span>
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
              <div className="text-sm text-base-content/60">
                Dashboard ID: <MaskedCredentialId value={masked} />
              </div>
            </div>
            <div className="grid gap-3 text-sm sm:grid-cols-2">
              {isViewer || isPersonal ? (
                <Meta
                  label="Monthly limit"
                  value={formatMonthlyCostLimit(
                    monthlyLimitCents > 0 ? monthlyLimitCents : keyRecord.monthly_cost_limit,
                  )}
                />
              ) : (
                <>
                  <Meta label="Daily limit" value={formatDailyCostLimit(dailyLimitCents)} />
                  {monthlyLimitCents > 0 ? (
                    <Meta label="Monthly limit" value={formatMonthlyCostLimit(monthlyLimitCents)} />
                  ) : null}
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
            </div>
          </div>
        </div>
      ) : null}

      {keyRecord && !notFound ? (
        <>
          {setupMode ? (
            <div className="glass-panel space-y-5 p-5 lg:p-6">
              <div className="space-y-2">
                <h2 className="text-lg font-semibold">Get set up</h2>
                <p className="text-sm text-base-content/70">
                  It looks like you haven&apos;t used this key yet. Point your SDK at the proxy with the snippets
                  below — once we see your first request, usage and spend stats will show up here automatically.
                </p>
              </div>
              {sdkBaseUrl ? (
                <ProxyKeyUsagePanel
                  provider={keyRecord.provider}
                  baseUrl={sdkBaseUrl}
                  proxyKey={keyRecord.key}
                  embedded
                />
              ) : (
                <p className="text-sm text-base-content/60">
                  SDK base URL is unavailable — refresh the page or open this key from the API Keys list.
                </p>
              )}
              <div className="flex flex-wrap items-center gap-3 border-t border-base-300/60 pt-4">
                <button type="button" className="btn btn-ghost btn-sm" onClick={dismissSetup}>
                  Setup already?
                </button>
              </div>
            </div>
          ) : (
            <>
              <SpendOverview
                todayUsd={costToday?.spend_usd ?? 0}
                monthUsd={costMonth?.spend_usd ?? 0}
                monthLabel={monthLabel}
                dailyLimitCents={dailyLimitCents}
                monthlyLimitCents={monthlyLimitCents}
                costSource={costSource}
                monthSource={monthSource}
                showDailyLimit={!isPersonal}
              />

              <div className={`grid gap-4 sm:grid-cols-2 ${isViewer ? "lg:grid-cols-3" : "lg:grid-cols-4"}`}>
                <LiveStat
                  title="Requests today"
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
                <LiveStat
                  title="Input tokens"
                  value={(costToday?.input_tokens ?? 0).toLocaleString()}
                  hint="today"
                  source={costSource}
                />
                {!isViewer ? (
                  <LiveStat
                    title="Rate usage"
                    value={rateRequestTotal.toLocaleString()}
                    hint="requests in live windows"
                    source={rateSource}
                  />
                ) : null}
              </div>

              <div className="glass-panel overflow-hidden">
                <div className="border-b border-base-300/70 bg-base-100/70 p-2">
                  <div role="tablist" className="flex flex-wrap gap-2" aria-label="Key detail sections">
                    <button
                      type="button"
                      role="tab"
                      aria-selected={tab === "usage"}
                      className={detailTabClass(tab === "usage")}
                      onClick={() => setTab("usage")}
                    >
                      Usage
                    </button>
                    <button
                      type="button"
                      role="tab"
                      aria-selected={tab === "cost"}
                      className={detailTabClass(tab === "cost")}
                      onClick={() => setTab("cost")}
                    >
                      Cost
                      {recentCost.length > 0 ? (
                        <span className="badge badge-ghost badge-sm border-0">{recentCost.length}</span>
                      ) : null}
                    </button>
                    <button
                      type="button"
                      role="tab"
                      aria-selected={tab === "pii"}
                      className={detailTabClass(tab === "pii")}
                      onClick={() => setTab("pii")}
                    >
                      PII
                      {recentPii.length > 0 ? (
                        <span className="badge badge-ghost badge-sm border-0">{recentPii.length}</span>
                      ) : null}
                    </button>
                    {!isViewer ? (
                      <button
                        type="button"
                        role="tab"
                        aria-selected={tab === "rate-limits"}
                        className={detailTabClass(tab === "rate-limits")}
                        onClick={() => setTab("rate-limits")}
                      >
                        Rate limits
                        {rateUsage.length > 0 ? (
                          <span className="badge badge-ghost badge-sm border-0">{rateUsage.length}</span>
                        ) : null}
                      </button>
                    ) : null}
                  </div>
                </div>

                <div className="space-y-4 p-4 lg:p-5">
                  {tab === "usage" ? (
                    sdkBaseUrl ? (
                      <ProxyKeyUsagePanel
                        provider={keyRecord.provider}
                        baseUrl={sdkBaseUrl}
                        proxyKey={keyRecord.key}
                        embedded
                      />
                    ) : (
                      <p className="text-sm text-base-content/60">
                        SDK base URL is unavailable — refresh the page or open this key from the API Keys list.
                      </p>
                    )
                  ) : null}

                  {tab === "cost" ? (
                    <>
                      {stats?.rollup_available && costHistory.length > 0 ? (
                        <ChartCard
                          title="Spend over time"
                          subtitle={`Last 7 days · ${DAILY_HISTORY_SUBTITLE}`}
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

                      <DetailSection
                        title="Cost breakdown"
                        subtitle={
                          stats?.rollup_available
                            ? "Fleet rollups from Redis · recent events are memory-only (last 50)"
                            : "In-process tracked spend · recent events are memory-only (last 50)"
                        }
                        source={costSource}
                      >
                        <div className="grid gap-4 p-5 lg:grid-cols-2">
                          <SpendPeriodPanel
                            title="Today"
                            subtitle="UTC calendar day"
                            source={costSource}
                            spentUsd={costToday?.spend_usd ?? 0}
                            limitCents={isPersonal ? undefined : dailyLimitCents}
                            limitLabel={isPersonal ? undefined : "Daily limit"}
                          >
                            <div className="grid gap-3 sm:grid-cols-2">
                              <Meta label="Input spend" value={formatUsd(costToday?.input_spend_usd ?? 0)} />
                              <Meta label="Output spend" value={formatUsd(costToday?.output_spend_usd ?? 0)} />
                              <Meta label="Requests" value={costToday?.requests ?? 0} />
                              <Meta label="Input tokens" value={(costToday?.input_tokens ?? 0).toLocaleString()} />
                              <Meta label="Output tokens" value={(costToday?.output_tokens ?? 0).toLocaleString()} />
                            </div>
                          </SpendPeriodPanel>
                          <SpendPeriodPanel
                            title="This month"
                            subtitle={monthLabel}
                            source={monthSource}
                            spentUsd={costMonth?.spend_usd ?? 0}
                            limitCents={monthlyLimitCents}
                            limitLabel="Monthly limit"
                          >
                            <p className="text-sm text-base-content/60">
                              Month-to-date total includes today. Prior days are archived in Redis; today may
                              include live in-process spend before flush.
                            </p>
                          </SpendPeriodPanel>
                        </div>
                        <KeyCostEventsTable rows={recentCost} />
                      </DetailSection>
                    </>
                  ) : null}

                  {tab === "pii" ? (
                    <>
                      {stats?.rollup_available && piiHistory.length > 0 ? (
                        <ChartCard
                          title="PII detections over time"
                          subtitle={`Last 7 days · ${DAILY_HISTORY_SUBTITLE}`}
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

                      <DetailSection
                        title="PII redaction"
                        subtitle={
                          stats?.rollup_available
                            ? "Fleet-wide Redis count · recent events are memory-only (last 50)"
                            : "Recent events are memory-only (last 50)"
                        }
                        source={piiSource}
                      >
                        <div className="grid gap-4 p-5 sm:grid-cols-2 lg:grid-cols-4">
                          <Meta label="Detections today" value={piiToday?.detections ?? 0} />
                          <Meta label="Recent events" value={recentPii.length} />
                          {!isViewer ? (
                            <>
                              <Meta label="Global fail mode" value={piiQuery.data?.fail_mode ?? "—"} />
                              <Meta label="Per-key override" value={piiLabel(keyRecord.redact_pii)} />
                            </>
                          ) : null}
                        </div>
                        <KeyPiiEventsTable rows={recentPii} />
                      </DetailSection>
                    </>
                  ) : null}

                  {tab === "rate-limits" && !isViewer ? (
                    <DetailSection
                      title="Rate limits"
                      subtitle="Overrides from key config (DynamoDB); usage from rate-limit backend"
                      source={rateSource}
                    >
                      <div className="grid gap-4 p-5 sm:grid-cols-2 lg:grid-cols-4">
                        <Meta label="RPM override" value={formatLimit(rateOverride?.RequestsPerMinute ?? keyRecord.rate_limit_rpm)} />
                        <Meta label="TPM override" value={formatLimit(rateOverride?.TokensPerMinute ?? keyRecord.rate_limit_tpm)} />
                        <Meta label="RPD override" value={formatLimit(rateOverride?.RequestsPerDay ?? keyRecord.rate_limit_rpd)} />
                        <Meta label="TPD override" value={formatLimit(rateOverride?.TokensPerDay ?? keyRecord.rate_limit_tpd)} />
                      </div>
                      <KeyRateUsageTable rows={rateUsage} />
                    </DetailSection>
                  ) : null}
                </div>
              </div>
            </>
          )}
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

function DetailSection({
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
