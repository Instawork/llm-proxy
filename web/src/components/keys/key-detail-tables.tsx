import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import DataTable from "../ui/data-table";
import { ProviderBadge } from "../ui/page-header";
import { formatCount, formatUsd } from "../../lib/format";
import type { CostRecentEvent, PIIRecentEvent } from "../../types";

function piiOutcomeBadge(outcome: PIIRecentEvent["outcome"]) {
  const map: Record<PIIRecentEvent["outcome"], string> = {
    ok: "badge-success",
    fail_open: "badge-warning",
    fail_closed: "badge-error",
    oversize: "badge-ghost",
  };
  return <span className={`badge badge-sm ${map[outcome]}`}>{outcome}</span>;
}

export function KeyCostEventsTable({ rows }: { rows: CostRecentEvent[] }) {
  const columns = useMemo<ColumnDef<CostRecentEvent, unknown>[]>(
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
        id: "model",
        accessorKey: "model",
        header: "Model",
        cell: ({ getValue }) => <span className="text-xs text-base-content/60">{getValue<string>() ?? "—"}</span>,
      },
      {
        id: "total",
        accessorKey: "spend_usd",
        header: "Total",
        cell: ({ getValue }) => formatUsd(getValue<number>()),
      },
      {
        id: "input",
        accessorFn: (row) => row.input_spend_usd ?? 0,
        header: "Input",
        cell: ({ getValue }) => formatUsd(getValue<number>()),
      },
      {
        id: "output",
        accessorFn: (row) => row.output_spend_usd ?? 0,
        header: "Output",
        cell: ({ getValue }) => formatUsd(getValue<number>()),
      },
      {
        id: "tokens",
        accessorFn: (row) => `${row.input_tokens}/${row.output_tokens}`,
        header: "Tokens",
        cell: ({ row }) => (
          <span className="text-base-content/70">
            {row.original.input_tokens}/{row.original.output_tokens}
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
      searchPlaceholder="Filter events…"
      emptyMessage="No cost events for this key yet"
      getRowId={(row, index) => `${row.time}-${row.model}-${index}`}
      tableClassName="table table-zebra border-t border-base-300/70"
    />
  );
}

export function KeyPiiEventsTable({ rows }: { rows: PIIRecentEvent[] }) {
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
          ) : (
            <span className="text-base-content/40">clean</span>
          ),
      },
      {
        id: "outcome",
        accessorKey: "outcome",
        header: "Outcome",
        cell: ({ getValue }) => piiOutcomeBadge(getValue<PIIRecentEvent["outcome"]>()),
      },
      {
        id: "latency",
        accessorKey: "duration_ms",
        header: "Latency",
        cell: ({ getValue }) => <span className="text-base-content/70">{getValue<number>().toFixed(1)} ms</span>,
      },
    ],
    [],
  );

  return (
    <DataTable
      data={rows}
      columns={columns}
      searchPlaceholder="Filter events…"
      emptyMessage="No PII events for this key yet"
      getRowId={(row, index) => `${row.time}-${index}`}
      tableClassName="table table-zebra border-t border-base-300/70"
    />
  );
}

export function KeyRateUsageTable({
  rows,
}: {
  rows: { window: string; requests: number; tokens: number }[];
}) {
  const columns = useMemo<ColumnDef<(typeof rows)[number], unknown>[]>(
    () => [
      {
        id: "window",
        accessorKey: "window",
        header: "Window",
        cell: ({ getValue }) => <span className="capitalize">{getValue<string>() === "day" ? "Today" : "Last minute"}</span>,
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
    [],
  );

  return (
    <DataTable
      data={rows}
      columns={columns}
      searchable={false}
      emptyMessage="No rate-limit usage recorded for this key"
      getRowId={(row) => row.window}
      tableClassName="table table-zebra border-t border-base-300/70"
    />
  );
}
