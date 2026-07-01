import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import DataTable from "../ui/data-table";
import { ProviderBadge } from "../ui/page-header";
import type { ProviderSpendAgg } from "../../lib/daily-history";
import { spendByProviderDisplayRows, type SpendByProviderDisplayRow } from "../../lib/group-rows";
import { formatCount, formatUsd } from "../../lib/format";
import { useCollapsedRows } from "../../hooks/use-collapsed-rows";

interface SpendByProviderTableProps {
  rows: ProviderSpendAgg[];
}

export default function SpendByProviderTable({ rows }: SpendByProviderTableProps) {
  const { displayData, onSearchActiveChange, footer } = useCollapsedRows(
    rows,
    spendByProviderDisplayRows,
    "providers",
  );

  const columns = useMemo<ColumnDef<SpendByProviderDisplayRow, unknown>[]>(
    () => [
      {
        id: "provider",
        accessorKey: "name",
        header: "Provider",
        cell: ({ row }) => {
          const data = row.original;
          if (data.isOthers) {
            return <span className="italic text-base-content/60">{data.name}</span>;
          }
          return <ProviderBadge provider={data.name} />;
        },
      },
      {
        id: "total",
        accessorKey: "spend_usd",
        header: "Total",
        meta: { alignRight: true },
        cell: ({ getValue }) => formatUsd(getValue<number>()),
      },
      {
        id: "requests",
        accessorKey: "requests",
        header: "Requests",
        meta: { alignRight: true },
        cell: ({ getValue }) => formatCount(getValue<number>()),
      },
    ],
    [],
  );

  return (
    <DataTable
      data={displayData}
      columns={columns}
      searchPlaceholder="Filter providers…"
      emptyMessage="No spend recorded for this window"
      getRowId={(row) => (row.isOthers ? "__others__" : row.name || String(row.requests))}
      onSearchActiveChange={onSearchActiveChange}
      footer={footer}
      initialSorting={[{ id: "total", desc: true }]}
    />
  );
}
