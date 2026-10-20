import { Link } from "react-router-dom";

import { BarChart, ChartCard } from "../charts";
import { chartPalette } from "../charts/chart-setup";
import { MaskedCredentialId } from "../ui/masked-credential-id";
import PageHeader, { ProviderBadge, StatusBadge } from "../ui/page-header";
import { SpendPeriodPanel } from "../ui/spend-breakdown";
import { DataSourceBadge } from "../ui/data-source";
import SetupGuidePanel from "./setup-guide-panel";
import { keyHasBeenUsed } from "../../lib/key-usage";
import { DAILY_HISTORY_SUBTITLE } from "../../lib/daily-history";
import { formatExpiresAt, isExpired } from "../../lib/key-form";
import {
  formatDailyCostLimit,
  formatMonthlyCostLimit,
  formatMonthYear,
  formatUsd,
} from "../../lib/format";
import type { APIKey, KeyStatsResponse } from "../../types";

function Meta({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div>
      <p className="text-xs uppercase tracking-wide text-base-content/50">{label}</p>
      <p className="font-medium">{value ?? "—"}</p>
    </div>
  );
}

/**
 * Read-only detail for a key shared with the current user: metadata and cost
 * only. No credential, copy/reveal, edit, delete, share, PII or rate-limit data.
 */
export default function SharedKeyDetail({
  keyRecord,
  stats,
}: {
  keyRecord: APIKey;
  stats?: KeyStatsResponse;
}) {
  const spend = keyRecord.spend;
  const masked = keyRecord.masked_key_id ?? keyRecord.key;
  const dailyCents = keyRecord.daily_cost_limit;
  const monthlyCents = keyRecord.monthly_cost_limit ?? 0;
  const history = stats?.cost_history ?? [];
  const today = spend?.today ?? stats?.cost_today;
  const month = spend?.month ?? stats?.cost_month;
  const todaySource = today?.source ?? "memory";
  const monthSource = month?.source ?? "memory";

  return (
    <div className="space-y-6">
      <div className="text-sm">
        <Link to="/keys" className="link link-hover text-base-content/60">
          ← API Keys
        </Link>
      </div>

      <PageHeader
        title={keyRecord.description?.trim() || "Unnamed key"}
        description="Shared with you as read-only. You can see this key's spend and limits, but not the key itself."
      />

      <div className="glass-panel p-5">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              <ProviderBadge provider={keyRecord.provider} />
              <StatusBadge active={keyRecord.enabled} activeLabel="Enabled" inactiveLabel="Disabled" />
              <span className="badge badge-ghost badge-sm">Read-only</span>
            </div>
            <div className="text-sm text-base-content/60">
              Key ID: <MaskedCredentialId value={masked} />
            </div>
          </div>
          <div className="grid gap-3 text-sm sm:grid-cols-2">
            <Meta label="Daily limit" value={formatDailyCostLimit(dailyCents)} />
            <Meta label="Monthly limit" value={formatMonthlyCostLimit(monthlyCents)} />
            <Meta label="Created" value={new Date(keyRecord.created_at).toLocaleString()} />
            <Meta
              label="Expires"
              value={
                <span className={isExpired(keyRecord.expires_at) ? "text-warning" : undefined}>
                  {formatExpiresAt(keyRecord.expires_at)}
                </span>
              }
            />
          </div>
        </div>
      </div>

      {stats?.rollup_available && history.length > 0 ? (
        <ChartCard title="Spend over time" subtitle={`Last 7 days · ${DAILY_HISTORY_SUBTITLE}`} source="redis">
          <BarChart
            labels={history.map((p) => p.day.slice(5))}
            values={history.map((p) => p.value)}
            label="Daily spend (USD)"
            colors={history.map(() => chartPalette.primary())}
          />
        </ChartCard>
      ) : null}

      <div className="glass-panel overflow-hidden">
        <div className="border-b border-base-300/70 px-5 py-4">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="font-semibold">Spend</h3>
            <DataSourceBadge source={todaySource} />
          </div>
        </div>
        <div className="grid gap-4 p-5 lg:grid-cols-2">
          <SpendPeriodPanel
            title="Today"
            subtitle="UTC calendar day"
            source={todaySource}
            spentUsd={today?.spend_usd ?? 0}
            limitCents={spend?.cap?.period === "daily" ? spend.cap.cents : undefined}
            limitLabel={spend?.cap?.period === "daily" ? "Daily limit" : undefined}
          >
            <div className="grid gap-3 sm:grid-cols-2">
              <Meta label="Input spend" value={formatUsd(today?.input_spend_usd ?? 0)} />
              <Meta label="Output spend" value={formatUsd(today?.output_spend_usd ?? 0)} />
              <Meta label="Requests" value={(today?.requests ?? 0).toLocaleString()} />
              <Meta label="Input tokens" value={(today?.input_tokens ?? 0).toLocaleString()} />
              <Meta label="Output tokens" value={(today?.output_tokens ?? 0).toLocaleString()} />
            </div>
          </SpendPeriodPanel>
          <SpendPeriodPanel
            title="This month"
            subtitle={formatMonthYear(month?.month)}
            source={monthSource}
            spentUsd={month?.spend_usd ?? 0}
            limitCents={spend?.cap?.period === "monthly" ? spend.cap.cents : undefined}
            limitLabel={spend?.cap?.period === "monthly" ? "Monthly limit" : undefined}
          />
        </div>
      </div>

      {!keyHasBeenUsed(keyRecord, stats ?? { cost_today: today, cost_month: month }) ? (
        <SetupGuidePanel
          shared
          dismissId={masked}
          provider={keyRecord.provider}
          baseUrl={keyRecord.base_url}
        />
      ) : null}
    </div>
  );
}
