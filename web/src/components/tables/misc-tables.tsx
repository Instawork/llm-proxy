import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import KeyLink from "../ui/key-link";
import DataTable from "../ui/data-table";
import { ProviderBadge, StatusBadge } from "../ui/page-header";
import { nameCountDisplayRows, type NameCountDisplayRow } from "../../lib/group-rows";
import {
  piiKeyPrimaryLabel,
  piiKeySecondaryLabel,
  piiKeyShowSecondary,
} from "../../lib/pii-key-display";
import { useCollapsedRows } from "../../hooks/use-collapsed-rows";
import type { APIKey, PIIRecentEvent } from "../../types";
import type { NameCount } from "../../lib/daily-history";

function outcomeBadge(outcome: PIIRecentEvent["outcome"]) {
  const map: Record<PIIRecentEvent["outcome"], string> = {
    ok: "badge-success",
    fail_open: "badge-warning",
    fail_closed: "badge-error",
    oversize: "badge-ghost",
  };
  return <span className={`badge badge-sm ${map[outcome]}`}>{outcome}</span>;
}

export function TopKeysTable({ rows, keys }: { rows: NameCount[]; keys: APIKey[] }) {
  const { displayData, onSearchActiveChange, footer } = useCollapsedRows(
    rows,
    nameCountDisplayRows,
    "keys",
  );

  const columns = useMemo<ColumnDef<NameCountDisplayRow, unknown>[]>(
    () => [
      {
        id: "key",
        accessorKey: "name",
        header: "Key",
        cell: ({ row }) => {
          const data = row.original;
          if (data.isOthers) {
            return <span className="italic text-base-content/60">{data.name}</span>;
          }
          const showSecondary = piiKeyShowSecondary(data.name, keys);
          return (
            <KeyLink
              keys={keys}
              maskedId={data.name}
              label={piiKeyPrimaryLabel(data.name, keys)}
              secondaryLabel={showSecondary ? piiKeySecondaryLabel(data.name, keys) : undefined}
              showMasked={showSecondary}
            />
          );
        },
      },
      {
        id: "count",
        accessorKey: "count",
        header: "Detections",
        meta: { alignRight: true },
        cell: ({ getValue }) => getValue<number>().toLocaleString(),
      },
    ],
    [keys],
  );

  return (
    <DataTable
      data={displayData}
      columns={columns}
      searchPlaceholder="Filter keys…"
      emptyMessage="No detections"
      getRowId={(row) => (row.isOthers ? "__others__" : row.name)}
      onSearchActiveChange={onSearchActiveChange}
      footer={footer}
    />
  );
}

export function RecentDetectionsTable({ rows, keys }: { rows: PIIRecentEvent[]; keys: APIKey[] }) {
  const columns = useMemo<ColumnDef<PIIRecentEvent, unknown>[]>(
    () => [
      {
        id: "time",
        accessorFn: (row) => new Date(row.time * 1000).toLocaleTimeString(),
        header: "Time",
        cell: ({ getValue }) => <span className="whitespace-nowrap text-base-content/70">{getValue<string>()}</span>,
      },
      {
        id: "provider",
        accessorKey: "provider",
        header: "Provider",
        cell: ({ getValue }) => <ProviderBadge provider={getValue<string>()} />,
      },
      {
        id: "key",
        accessorKey: "key_id",
        header: "Key",
        cell: ({ row }) => {
          const keyId = row.original.key_id;
          if (!keyId) return "—";
          const showSecondary = piiKeyShowSecondary(keyId, keys);
          return (
            <KeyLink
              keys={keys}
              maskedId={keyId}
              label={piiKeyPrimaryLabel(keyId, keys)}
              secondaryLabel={showSecondary ? piiKeySecondaryLabel(keyId, keys) : undefined}
              showMasked={showSecondary}
              className="font-mono text-xs"
            />
          );
        },
      },
      {
        id: "entities",
        accessorKey: "entity_total",
        header: "Entities",
        enableSorting: false,
        cell: ({ row }) =>
          row.original.entity_total > 0 ? (
            <div className="flex flex-wrap gap-1">
              {Object.entries(row.original.entity_counts).map(([name, n]) => (
                <span key={name} className="badge badge-sm badge-outline">
                  {name.replaceAll("_", " ")} ×{n}
                </span>
              ))}
            </div>
          ) : row.original.outcome === "ok" ? (
            <span className="text-base-content/40">clean</span>
          ) : (
            // fail_open / fail_closed / oversize never reached the analyzer,
            // so "clean" would be misleading — the body was not scanned.
            <span className="text-base-content/40">not scanned</span>
          ),
      },
      {
        id: "outcome",
        accessorKey: "outcome",
        header: "Outcome",
        cell: ({ getValue }) => outcomeBadge(getValue<PIIRecentEvent["outcome"]>()),
      },
      {
        id: "latency",
        accessorKey: "duration_ms",
        header: "Latency",
        cell: ({ getValue }) => <span className="text-base-content/70">{getValue<number>().toFixed(1)} ms</span>,
      },
    ],
    [keys],
  );

  return (
    <DataTable
      data={rows}
      columns={columns}
      searchPlaceholder="Filter detections…"
      emptyMessage="No detections recorded yet"
      getRowId={(row, index) => `${row.time}-${index}`}
    />
  );
}

export type BlockedByKeyRow = {
  label: string;
  count: number;
  currentlyOpen: boolean;
};

export function BlockedByKeyTable({ rows }: { rows: BlockedByKeyRow[] }) {
  const columns = useMemo<ColumnDef<BlockedByKeyRow, unknown>[]>(
    () => [
      {
        id: "status",
        accessorKey: "currentlyOpen",
        header: "Now",
        cell: ({ getValue }) => {
          const open = getValue<boolean>();
          return (
            <span
              className={`badge badge-sm badge-outline ${open ? "badge-error" : "badge-success"}`}
            >
              {open ? "open" : "recovered"}
            </span>
          );
        },
      },
      {
        id: "label",
        accessorKey: "label",
        header: "Breaker key",
        cell: ({ row, getValue }) => (
          <span
            className={`font-mono text-xs ${row.original.currentlyOpen ? "" : "text-base-content/45"}`}
          >
            {getValue<string>()}
          </span>
        ),
      },
      {
        id: "count",
        accessorKey: "count",
        header: "Blocked today",
        meta: { alignRight: true },
        cell: ({ row, getValue }) => (
          <span className={row.original.currentlyOpen ? "" : "text-base-content/45"}>
            {getValue<number>().toLocaleString()}
          </span>
        ),
      },
    ],
    [],
  );

  return (
    <DataTable
      data={rows}
      columns={columns}
      searchPlaceholder="Filter breaker keys…"
      emptyMessage="No blocked requests in this window"
      getRowId={(row) => row.label}
      tableClassName="table table-zebra table-sm"
    />
  );
}

export function CircuitProvidersTable({
  names,
  providers,
  range,
  hasFailureRedis,
  providerFailurePeaks,
}: {
  names: string[];
  providers: Record<string, { state?: string; error?: string; failures?: number; rollup?: { open?: boolean; enabled?: boolean; threshold?: number } }>;
  range: string;
  hasFailureRedis: boolean;
  providerFailurePeaks: Map<string, number>;
}) {
  const rows = useMemo(
    () =>
      names.map((name) => {
        const p = providers[name];
        const state = p.state ?? p.error ?? "unknown";
        const failures =
          range === "today" || !hasFailureRedis ? (p.failures ?? "—") : (providerFailurePeaks.get(name) ?? 0);
        return {
          name,
          state,
          failures,
          rollup: p.rollup?.open ? "open" : p.rollup?.enabled ? "closed" : "—",
          threshold: p.rollup?.threshold ?? "—",
        };
      }),
    [names, providers, range, hasFailureRedis, providerFailurePeaks],
  );

  const columns = useMemo<ColumnDef<(typeof rows)[number], unknown>[]>(
    () => [
      {
        id: "provider",
        accessorKey: "name",
        header: "Provider",
        cell: ({ getValue }) => <ProviderBadge provider={getValue<string>()} />,
      },
      {
        id: "state",
        accessorKey: "state",
        header: "State",
        cell: ({ getValue }) => {
          const state = getValue<string>();
          return <StatusBadge active={state === "closed"} activeLabel="closed" inactiveLabel={state} />;
        },
      },
      {
        id: "failures",
        accessorKey: "failures",
        header: range === "today" ? "Failures" : "Peak failures",
      },
      {
        id: "rollup",
        accessorKey: "rollup",
        header: "Rollup",
      },
      {
        id: "threshold",
        accessorKey: "threshold",
        header: "Threshold",
      },
    ],
    [range],
  );

  return (
    <DataTable
      data={rows}
      columns={columns}
      searchPlaceholder="Filter providers…"
      emptyMessage="No provider data"
      getRowId={(row) => row.name}
    />
  );
}

export function CircuitActivityTable({
  events,
  formatEventTime,
  eventLabel,
  parseBreakerKey,
}: {
  events: import("../../types").CircuitActivityEvent[];
  formatEventTime: (unix: number) => string;
  eventLabel: (kind: string) => string;
  parseBreakerKey: (key: string | undefined, fallback: string) => { model?: string | null; scope: string };
}) {
  const columns = useMemo<ColumnDef<import("../../types").CircuitActivityEvent, unknown>[]>(
    () => [
      {
        id: "time",
        accessorKey: "time",
        header: "Time",
        cell: ({ getValue }) => (
          <span className="whitespace-nowrap text-xs">{formatEventTime(getValue<number>())}</span>
        ),
      },
      {
        id: "event",
        accessorKey: "kind",
        header: "Event",
        cell: ({ getValue }) => eventLabel(getValue<string>()),
      },
      {
        id: "provider",
        accessorKey: "provider",
        header: "Provider",
        cell: ({ getValue }) => <ProviderBadge provider={getValue<string>()} />,
      },
      {
        id: "model",
        accessorFn: (row) => parseBreakerKey(row.key, row.provider).model,
        header: "Model",
        cell: ({ getValue }) => <span className="font-mono text-xs">{getValue<string>() ?? "—"}</span>,
      },
      {
        id: "scope",
        accessorFn: (row) => parseBreakerKey(row.key, row.provider).scope,
        header: "Scope",
        cell: ({ row }) => {
          const scope = parseBreakerKey(row.original.key, row.original.provider).scope;
          return (
            <span className={`badge badge-sm ${scope === "model" ? "badge-ghost" : "badge-warning"}`}>
              {scope === "model" ? "per-model" : "provider-wide"}
            </span>
          );
        },
      },
      {
        id: "result",
        accessorKey: "new_state",
        header: "Result",
        cell: ({ getValue }) => {
          const state = getValue<string | undefined>();
          return state ? (
            <StatusBadge active={state === "closed"} activeLabel="closed" inactiveLabel={state} />
          ) : (
            "—"
          );
        },
      },
      {
        id: "detail",
        accessorFn: (row) => row.reason ?? "",
        header: "Detail",
        enableSorting: false,
        cell: ({ row }) => {
          const e = row.original;
          const { scope } = parseBreakerKey(e.key, e.provider);
          return (
            <span className="text-xs text-base-content/70">
              {e.status_code ? `HTTP ${e.status_code}` : null}
              {e.failure_kind ? ` ${e.failure_kind}` : null}
              {e.upstream_error ? (
                <span className="block text-base-content/60">{e.upstream_error}</span>
              ) : null}
              {e.reason ? e.reason : null}
              {scope === "model" && e.key ? (
                <span className="block font-mono text-base-content/50">{e.key}</span>
              ) : null}
            </span>
          );
        },
      },
    ],
    [formatEventTime, eventLabel, parseBreakerKey],
  );

  return (
    <DataTable
      data={events}
      columns={columns}
      searchPlaceholder="Filter events…"
      emptyMessage="No recovery probes in this window"
      tableClassName="table table-zebra table-sm"
      getRowId={(row, index) => `${row.time}-${row.kind}-${index}`}
    />
  );
}
