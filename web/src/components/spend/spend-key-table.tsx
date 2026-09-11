import { useMemo } from "react";
import { Link } from "react-router-dom";
import type { ColumnDef } from "@tanstack/react-table";

import DataTable from "../ui/data-table";
import { MaskedCredentialId } from "../ui/masked-credential-id";
import { ProviderBadge } from "../ui/page-header";
import { formatCount, formatUsd } from "../../lib/format";
import { keyDetailPathForMaskedId } from "../../lib/key-routes";
import { UNMETERED_HELP, capFraction, unmeteredEndpointSummary } from "../../lib/spend-overview";
import type { SpendKeyRow } from "../../types";

export default function SpendKeyTable({
  rows,
  monthLabel,
  showOwner,
}: {
  rows: SpendKeyRow[];
  monthLabel: string;
  showOwner: boolean;
}) {
  const columns = useMemo<ColumnDef<SpendKeyRow, unknown>[]>(() => {
    const cols: ColumnDef<SpendKeyRow, unknown>[] = [
      {
        id: "key",
        accessorFn: (row) => `${row.description} ${row.key_id}`,
        header: "Key",
        cell: ({ row }) => (
          <Link
            to={keyDetailPathForMaskedId(row.original.key_id)}
            className={`link link-hover link-primary no-underline ${row.original.enabled ? "" : "opacity-60"}`}
          >
            <span className="font-medium">{row.original.description || row.original.key_id}</span>
            <MaskedCredentialId value={row.original.key_id} className="mt-0.5 block opacity-70" />
          </Link>
        ),
      },
      {
        id: "provider",
        accessorKey: "provider",
        header: "Provider",
        cell: ({ getValue }) => <ProviderBadge provider={getValue<string>()} />,
      },
    ];
    if (showOwner) {
      cols.push({
        id: "owner",
        accessorFn: (row) => row.owner_email ?? "",
        header: "Owner",
        cell: ({ getValue }) => (
          <span className="text-base-content/70">{getValue<string>() || <span className="italic">org</span>}</span>
        ),
      });
    }
    cols.push(
      {
        id: "today",
        accessorFn: (row) => row.today.spend_usd,
        header: "Today",
        meta: { alignRight: true },
        cell: ({ getValue }) => formatUsd(getValue<number>()),
      },
      {
        id: "month",
        accessorFn: (row) => row.month.spend_usd,
        header: monthLabel,
        meta: { alignRight: true },
        cell: ({ getValue }) => formatUsd(getValue<number>()),
      },
      {
        id: "requests",
        accessorFn: (row) => row.today.requests,
        header: "Requests today",
        meta: { alignRight: true },
        cell: ({ getValue }) => formatCount(getValue<number>()),
      },
      {
        id: "unmetered",
        accessorFn: (row) => row.unmetered.requests,
        header: "Unmetered",
        meta: { alignRight: true },
        cell: ({ row }) => {
          const stats = row.original.unmetered;
          if (stats.requests === 0) return <span className="text-base-content/40">0</span>;
          return (
            <span title={`${unmeteredEndpointSummary(stats, 5)}\n\n${UNMETERED_HELP}`}>
              {formatCount(stats.requests)}
            </span>
          );
        },
      },
      {
        id: "cap",
        accessorFn: (row) => capFraction(row)?.fraction ?? -1,
        header: "Of limit",
        meta: { alignRight: true },
        cell: ({ row }) => {
          const cap = capFraction(row.original);
          if (!cap) return <span className="text-base-content/40">Unlimited</span>;
          const tone = cap.fraction >= 0.95 ? "text-error" : cap.fraction >= 0.8 ? "text-warning" : "";
          return (
            <span className={tone} title={`${formatUsd(cap.spentUsd)} of ${formatUsd(cap.limitCents / 100, 2)} (${cap.label.toLowerCase()})`}>
              {(cap.fraction * 100).toFixed(0)}%
            </span>
          );
        },
      },
    );
    return cols;
  }, [monthLabel, showOwner]);

  return (
    <DataTable
      data={rows}
      columns={columns}
      searchPlaceholder="Filter keys…"
      emptyMessage="No keys"
      getRowId={(row) => row.key_id}
      initialSorting={[{ id: "month", desc: true }]}
    />
  );
}
