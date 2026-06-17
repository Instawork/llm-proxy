import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import { BarChart, ChartCard } from "../components/charts";
import { chartPalette } from "../components/charts/chart-setup";
import KeyLink from "../components/ui/key-link";
import DataTable from "../components/ui/data-table";
import { LiveStat, rateLimitUsageSource, SectionPanel } from "../components/ui/data-source";
import PageHeader, { ErrorAlert, LiveIndicator, LoadingBlock } from "../components/ui/page-header";
import { useKeys, useRateLimits } from "../hooks/queries";
import { scopeUsageDisplayRows } from "../lib/group-rows";
import { useCollapsedRows } from "../hooks/use-collapsed-rows";
import { formatCount, scopeLabel } from "../lib/format";
import type { RateLimitCounter } from "../types";

interface ScopeRow {
  scope: string;
  label: string;
  requests: number;
  tokens: number;
}

function counterRows(counters: Record<string, RateLimitCounter> | undefined): ScopeRow[] {
  return Object.entries(counters ?? {}).map(([scope, c]) => ({
    scope,
    label: scopeLabel(scope),
    requests: c.requests ?? 0,
    tokens: c.tokens ?? 0,
  }));
}

function RateLimitUsageTable({ rows, keys }: { rows: ScopeRow[]; keys: ReturnType<typeof useKeys>["data"] }) {
  const { displayData, onSearchActiveChange, footer } = useCollapsedRows(
    rows,
    scopeUsageDisplayRows,
    "scopes",
  );

  const columns = useMemo<ColumnDef<(typeof displayData)[number], unknown>[]>(
    () => [
      {
        id: "scope",
        accessorKey: "label",
        header: "Scope",
        cell: ({ row }) => {
          const data = row.original;
          if (data.isOthers) {
            return <span className="italic text-base-content/60">{data.label}</span>;
          }
          return data.scope.startsWith("key:") ? (
            <KeyLink keys={keys} scope={data.scope} label={data.label} />
          ) : (
            <span className="font-medium">{data.label}</span>
          );
        },
      },
      {
        id: "requests",
        accessorKey: "requests",
        header: "Requests",
        cell: ({ getValue }) => formatCount(getValue<number>()),
      },
      {
        id: "tokens",
        accessorKey: "tokens",
        header: "Tokens",
        cell: ({ getValue }) => formatCount(getValue<number>()),
      },
    ],
    [keys],
  );

  return (
    <DataTable
      data={displayData}
      columns={columns}
      searchPlaceholder="Filter scopes…"
      emptyMessage="No usage recorded in this window"
      getRowId={(row) => (row.isOthers ? "__others__" : row.scope)}
      onSearchActiveChange={onSearchActiveChange}
      footer={footer}
    />
  );
}

export default function RateLimitsPage() {
  const { data, isLoading, error, dataUpdatedAt, isFetching, refetch } = useRateLimits();
  const keys = useKeys();
  const [window, setWindow] = useState<"day" | "minute">("day");

  if (isLoading) return <LoadingBlock />;
  if (error) {
    return <ErrorAlert message={error instanceof Error ? error.message : "Failed to load rate limits"} />;
  }
  if (!data) return null;

  const win = data.snapshot?.[window];
  const rows = counterRows(win?.counters);
  const limits = data.limits;
  const usageSource = rateLimitUsageSource(data.backend);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Rate Limits"
        description="Configured limits and live per-scope usage."
        actions={
          <LiveIndicator updatedAt={dataUpdatedAt} fetching={isFetching} onRefresh={() => refetch()} />
        }
      />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <LiveStat title="Enabled" value={data.enabled ? "Yes" : "No"} source="config" />
        <LiveStat
          title="Backend"
          value={data.backend ?? "memory"}
          hint="Separate from admin rollup Redis"
          source={usageSource}
          valueClassName="text-lg"
        />
        <LiveStat
          title="Active scopes"
          value={rows.length}
          hint={`${window} window`}
          source={usageSource}
        />
        <LiveStat
          title="Overrides"
          value={
            Object.keys(data.overrides?.PerUser ?? {}).length +
            Object.keys(data.overrides?.PerKey ?? {}).length +
            Object.keys(data.overrides?.PerModel ?? {}).length
          }
          source="config"
        />
      </div>

      <div className="flex items-center gap-3">
        <span className="text-sm text-base-content/60">Window:</span>
        <div role="tablist" className="tabs tabs-boxed rounded-xl bg-base-100/80 p-1 ring-1 ring-base-300/60">
          {(["day", "minute"] as const).map((w) => (
            <button
              key={w}
              type="button"
              role="tab"
              className={`tab rounded-lg px-4 ${window === w ? "tab-active font-medium" : ""}`}
              onClick={() => setWindow(w)}
            >
              {w === "day" ? "Daily" : "Per-minute"}
            </button>
          ))}
        </div>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <ChartCard title="Requests by scope" subtitle={`${window} window`} source={usageSource}>
          <BarChart
            labels={rows.map((r) => r.label)}
            values={rows.map((r) => r.requests)}
            label="Requests"
            horizontal
          />
        </ChartCard>
        <ChartCard title="Tokens by scope" subtitle={`${window} window`} source={usageSource}>
          <BarChart
            labels={rows.map((r) => r.label)}
            values={rows.map((r) => r.tokens)}
            label="Tokens"
            colors={rows.map(() => chartPalette.info())}
            horizontal
          />
        </ChartCard>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <SectionPanel title="Default limits" source="config">
          <div className="grid gap-4 p-5 sm:grid-cols-2 lg:grid-cols-4">
            <Limit label="RPM" value={limits?.RequestsPerMinute} />
            <Limit label="TPM" value={limits?.TokensPerMinute} />
            <Limit label="RPD" value={limits?.RequestsPerDay} />
            <Limit label="TPD" value={limits?.TokensPerDay} />
          </div>
        </SectionPanel>

        <SectionPanel title={`Live usage (${window})`} source={usageSource}>
          <RateLimitUsageTable rows={rows} keys={keys.data} />
        </SectionPanel>
      </div>
    </div>
  );
}

function Limit({ label, value }: { label: string; value?: number }) {
  return (
    <div>
      <p className="text-xs uppercase tracking-wide text-base-content/50">{label}</p>
      <p className="text-lg font-medium">{value ? value : "∞"}</p>
    </div>
  );
}
