import { Link } from "react-router-dom";

import PageHeader, { ErrorAlert, LiveIndicator, LoadingBlock, ProviderBadge } from "../components/ui/page-header";
import { SpendOverview } from "../components/ui/spend-breakdown";
import { SpendLimitProgress } from "../components/ui/spend-limit-progress";
import { useSpendOverview } from "../hooks/queries";
import { formatMonthYear, formatUsd } from "../lib/format";
import { keyDetailPathForMaskedId } from "../lib/key-routes";

export default function OverviewPage() {
  const overview = useSpendOverview();

  if (overview.isLoading) return <LoadingBlock />;
  if (overview.error) {
    return (
      <ErrorAlert
        message={overview.error instanceof Error ? overview.error.message : "Failed to load spend overview"}
      />
    );
  }
  const data = overview.data;
  if (!data) return null;

  const monthLabel = formatMonthYear(data.month);
  const mine = data.scope === "mine";

  return (
    <div className="space-y-6">
      <PageHeader
        title="Overview"
        description={
          mine
            ? "What your API keys have spent today and this month."
            : "What the fleet has spent today and this month, by key and by provider."
        }
        actions={
          <LiveIndicator
            updatedAt={overview.dataUpdatedAt}
            fetching={overview.isFetching}
            onRefresh={() => overview.refetch()}
          />
        }
      />

      <SpendOverview
        todayUsd={data.totals.today.spend_usd}
        monthUsd={data.totals.month.spend_usd}
        monthLabel={monthLabel}
        dailyLimitCents={0}
        monthlyLimitCents={0}
        costSource={data.totals.today.source}
        monthSource={data.totals.month.source}
        showDailyLimit={false}
        showMonthlyLimit={false}
      />

      <div className="glass-panel p-5">
        <h3 className="mb-4 font-semibold">{mine ? "Your keys" : "Keys"}</h3>
        {data.keys.length === 0 ? (
          <p className="text-sm text-base-content/60">
            No keys yet. <Link to="/keys" className="link link-primary">Create one</Link> to start tracking spend.
          </p>
        ) : (
          <ul className="divide-y divide-base-300/70">
            {data.keys.map((row) => (
              <li key={row.key_id} className="flex flex-wrap items-center justify-between gap-3 py-3">
                <div className="min-w-0">
                  <Link to={keyDetailPathForMaskedId(row.key_id)} className="link link-hover link-primary font-medium no-underline">
                    {row.description || row.key_id}
                  </Link>
                  <div className="mt-1 flex items-center gap-2 text-xs text-base-content/60">
                    <ProviderBadge provider={row.provider} />
                    {row.owner_email ? <span>{row.owner_email}</span> : null}
                  </div>
                </div>
                <div className="grid min-w-64 grid-cols-2 gap-4 text-right">
                  <div>
                    <p className="text-xs text-base-content/50">Today</p>
                    <p className="font-semibold">{formatUsd(row.today.spend_usd)}</p>
                  </div>
                  <div>
                    <p className="text-xs text-base-content/50">{monthLabel}</p>
                    <p className="font-semibold">{formatUsd(row.month.spend_usd)}</p>
                  </div>
                  {row.cap ? (
                    <div className="col-span-2">
                      <SpendLimitProgress
                        spentUsd={row.cap.period === "monthly" ? row.month.spend_usd : row.today.spend_usd}
                        limitCents={row.cap.cents}
                        label={row.cap.period === "monthly" ? "Monthly limit" : "Daily limit"}
                      />
                    </div>
                  ) : null}
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
