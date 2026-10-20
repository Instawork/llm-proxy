import { Link, useSearchParams } from "react-router-dom";

import { BarChart, ChartCard, DonutChart } from "../components/charts";
import { chartPalette } from "../components/charts/chart-setup";
import SpendKeyCard from "../components/spend/spend-key-card";
import SpendKeyTable from "../components/spend/spend-key-table";
import SpendPeriodTable from "../components/spend/spend-period-table";
import { UnmeteredEndpointList } from "../components/spend/unmetered-calls";
import { LiveStat, SectionPanel } from "../components/ui/data-source";
import PageHeader, { ErrorAlert, LiveIndicator, LoadingBlock, ProviderBadge } from "../components/ui/page-header";
import { SpendOverview } from "../components/ui/spend-breakdown";
import { useMe, useSpendOverview } from "../hooks/queries";
import { compact, formatCount, formatMonthYear, formatUsd, scopeLabel } from "../lib/format";
import { donutSlices } from "../lib/group-rows";
import {
  CAVEAT_COPY,
  DEFAULT_SPEND_VIEW,
  SPEND_KEY_CARD_LIMIT,
  SPEND_VIEW_OPTIONS,
  UNMETERED_HELP,
  overviewScopeCopy,
  parseSpendView,
  sortBySpend,
  type SpendView,
} from "../lib/spend-overview";
import type { SpendOverviewResponse, SpendProviderRow, SpendUserRow } from "../types";

const PROVIDER_COLORS = [
  chartPalette.primary,
  chartPalette.info,
  chartPalette.success,
  chartPalette.warning,
  chartPalette.error,
];

export default function OverviewPage() {
  const { data: me } = useMe();
  const [searchParams, setSearchParams] = useSearchParams();
  const isViewer = (me?.role ?? "viewer") === "viewer";
  const view = parseSpendView(searchParams.get("scope"));
  const overview = useSpendOverview(isViewer ? view : undefined);

  const setView = (next: SpendView) => {
    setSearchParams(
      (prev) => {
        const params = new URLSearchParams(prev);
        if (next === DEFAULT_SPEND_VIEW) params.delete("scope");
        else params.set("scope", next);
        return params;
      },
      { replace: true },
    );
  };

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
  const activeView = mine ? (data.view ?? view) : null;
  const sharedCount = data.keys.filter((row) => row.shared).length;
  const scope = overviewScopeCopy(activeView, { owned: data.keys.length - sharedCount, shared: sharedCount });
  const scopeSuffix = scope.scopePhrase ? ` · ${scope.scopePhrase}` : "";
  const keys = sortBySpend(data.keys, (row) => row.key_id);
  const providers = sortBySpend(data.providers, (row) => row.name);
  const providersWithSpend = providers.filter((p) => p.today.spend_usd > 0);
  const donut = donutSlices(
    providersWithSpend.map((p) => p.name),
    providersWithSpend.map((p) => p.today.spend_usd),
    providersWithSpend.map((_, i) => PROVIDER_COLORS[i % PROVIDER_COLORS.length]()),
    8,
    chartPalette.tick(),
  );

  return (
    <div className="space-y-6">
      <PageHeader
        title="Overview"
        description={
          mine
            ? "What your API keys and the keys shared with you have spent today and this month."
            : "What the fleet has spent today and this month, by key, provider, and user."
        }
        actions={
          <LiveIndicator
            updatedAt={overview.dataUpdatedAt}
            fetching={overview.isFetching}
            onRefresh={() => overview.refetch()}
          />
        }
      />

      {data.caveats.length > 0 ? (
        <div className="space-y-1 text-xs text-base-content/60">
          {data.caveats.map((c) => (
            <p key={c}>{CAVEAT_COPY[c]}</p>
          ))}
        </div>
      ) : null}

      {mine ? <SpendViewFilter value={activeView ?? view} onChange={setView} /> : null}

      <SpendOverview
        title={scope.totalsTitle}
        note={scope.excludesNote}
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

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        <LiveStat
          title="Requests today"
          value={formatCount(data.totals.today.requests)}
          hint={`UTC day${scopeSuffix}`}
          source={data.totals.today.source}
        />
        <LiveStat
          title="Tokens today"
          value={compact(data.totals.today.input_tokens + data.totals.today.output_tokens)}
          hint={`${compact(data.totals.today.input_tokens)} in · ${compact(data.totals.today.output_tokens)} out`}
          source={data.totals.today.source}
        />
        <LiveStat
          title={mine ? "Your keys" : "Keys"}
          value={keys.length}
          hint={mine ? `${keys.length - sharedCount} owned · ${sharedCount} shared` : "tracked"}
          source="dynamodb"
        />
        <LiveStat
          title="Providers"
          value={providersWithSpend.length}
          hint="with spend today"
          source={data.totals.today.source}
        />
        <LiveStat
          title="Unmetered calls"
          value={formatCount(data.unmetered.requests)}
          hint={`no token usage, not billed${scopeSuffix}`}
          source={data.unmetered.source}
        />
      </div>

      <section className="space-y-3">
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-lg font-semibold">{mine ? "Your keys" : "Spend by key"}</h2>
          <Link to="/keys" className="link link-hover text-sm text-base-content/60">
            Manage keys
          </Link>
        </div>
        {keys.length === 0 ? (
          <div className="glass-panel p-5 text-sm text-base-content/60">
            No keys yet.{" "}
            <Link to="/keys" className="link link-primary">
              Create one
            </Link>{" "}
            to start tracking spend.
          </div>
        ) : keys.length <= SPEND_KEY_CARD_LIMIT ? (
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {keys.map((row) => (
              <SpendKeyCard key={row.key_id} row={row} monthLabel={monthLabel} scopeNote={scope.rowNote(row)} />
            ))}
          </div>
        ) : (
          <div className="glass-panel p-5">
            <SpendKeyTable rows={keys} monthLabel={monthLabel} showOwner={!mine} scopeNote={scope.rowNote} />
          </div>
        )}
      </section>

      <SectionPanel
        title="Unmetered calls"
        subtitle={mine ? `${UNMETERED_HELP} Counts cover ${scope.scopePhrase}.` : UNMETERED_HELP}
        source={data.unmetered.source}
      >
        <UnmeteredEndpointList stats={data.unmetered} />
      </SectionPanel>

      <div className="grid gap-4 xl:grid-cols-3">
        <ChartCard
          title="Today by provider"
          subtitle={`${formatUsd(data.totals.today.spend_usd)} across ${providersWithSpend.length} provider${providersWithSpend.length === 1 ? "" : "s"}${scopeSuffix}`}
          source={data.totals.today.source}
        >
          {donut.values.length ? (
            <DonutChart
              labels={donut.labels}
              values={donut.values}
              colors={donut.colors}
              centerLabel="Today"
              centerValue={formatUsd(data.totals.today.spend_usd)}
            />
          ) : (
            <p className="py-10 text-center text-sm text-base-content/60">No spend recorded today</p>
          )}
        </ChartCard>
        <div className="xl:col-span-2">
          <SectionPanel title="Spend by provider" subtitle={`Today and month-to-date per upstream provider${scopeSuffix}`}>
            <SpendPeriodTable<SpendProviderRow>
              rows={providers}
              monthLabel={monthLabel}
              nameHeader="Provider"
              rowId={(row) => row.name}
              renderName={(row) => <ProviderBadge provider={row.name} />}
              searchPlaceholder="Filter providers…"
              emptyMessage="No spend recorded"
            />
          </SectionPanel>
        </div>
      </div>

      <ChartCard
        title="Daily spend"
        subtitle={
          mine
            ? `UTC daily totals for ${scope.scopePhrase} (today updates live)`
            : "UTC daily totals for the fleet (today updates live)"
        }
        source={data.rollup_available ? "redis" : "memory"}
      >
        <BarChart
          labels={data.history.map((p) => p.day.slice(5))}
          values={data.history.map((p) => p.spend_usd)}
          label="Spend (USD)"
          colors={data.history.map(() => chartPalette.primary())}
        />
      </ChartCard>

      {data.fleet ? <FleetSection data={data} monthLabel={monthLabel} /> : null}
    </div>
  );
}

function SpendViewFilter({ value, onChange }: { value: SpendView; onChange: (next: SpendView) => void }) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <span className="text-sm text-base-content/60">Show spend for</span>
      <div role="group" aria-label="Spend scope" className="join">
        {SPEND_VIEW_OPTIONS.map((option) => (
          <button
            key={option.value}
            type="button"
            title={option.hint}
            aria-pressed={value === option.value}
            className={`btn btn-sm join-item ${value === option.value ? "btn-primary" : "btn-ghost border-base-300"}`}
            onClick={() => onChange(option.value)}
          >
            {option.label}
          </button>
        ))}
      </div>
    </div>
  );
}

function FleetSection({ data, monthLabel }: { data: SpendOverviewResponse; monthLabel: string }) {
  const fleet = data.fleet!;
  const users = sortBySpend(fleet.users, (row) => row.scope);
  const unattributed = fleet.unattributed;
  return (
    <SectionPanel
      title="Spend by user"
      subtitle="Request-scoped user ids reported to the proxy — not key owners"
    >
      <SpendPeriodTable<SpendUserRow>
        rows={users}
        monthLabel={monthLabel}
        nameHeader="User"
        rowId={(row) => row.scope}
        renderName={(row) => <span className="font-medium">{scopeLabel(row.scope)}</span>}
        searchPlaceholder="Filter users…"
        emptyMessage="No user-attributed spend recorded"
        footer={
          <div className="flex w-full flex-wrap items-center justify-between gap-3 text-sm">
            <span className="text-base-content/60">
              Not attributed to a registered key: {formatUsd(unattributed.today.spend_usd)} today,{" "}
              {formatUsd(unattributed.month.spend_usd)} this month
            </span>
            <Link to="/cost" className="link link-hover link-primary no-underline">
              Cost details →
            </Link>
          </div>
        }
      />
    </SectionPanel>
  );
}
