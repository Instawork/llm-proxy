import { useMemo, type ReactNode } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import DataTable from "../ui/data-table";
import { DataSourceBadge } from "../ui/data-source";
import { formatCount, formatUsd } from "../../lib/format";
import type { KeyCostMonthStats, KeyCostStats } from "../../types";

export interface SpendPeriodRow {
  today: KeyCostStats;
  month: KeyCostMonthStats;
}

/** Today / month-to-date table shared by the provider and user breakdowns. */
export default function SpendPeriodTable<T extends SpendPeriodRow>({
  rows,
  monthLabel,
  nameHeader,
  rowId,
  renderName,
  searchPlaceholder,
  emptyMessage,
  footer,
}: {
  rows: T[];
  monthLabel: string;
  nameHeader: string;
  rowId: (row: T) => string;
  renderName: (row: T) => ReactNode;
  searchPlaceholder: string;
  emptyMessage: string;
  footer?: ReactNode;
}) {
  const columns = useMemo<ColumnDef<T, unknown>[]>(
    () => [
      {
        id: "name",
        accessorFn: (row) => rowId(row),
        header: nameHeader,
        cell: ({ row }) => renderName(row.original),
      },
      {
        id: "today",
        accessorFn: (row) => row.today.spend_usd,
        header: "Today",
        meta: { alignRight: true },
        cell: ({ row }) => (
          <span className="inline-flex items-center gap-2">
            {formatUsd(row.original.today.spend_usd)}
            <DataSourceBadge source={row.original.today.source} />
          </span>
        ),
      },
      {
        id: "requests",
        accessorFn: (row) => row.today.requests,
        header: "Requests today",
        meta: { alignRight: true },
        cell: ({ getValue }) => formatCount(getValue<number>()),
      },
      {
        id: "month",
        accessorFn: (row) => row.month.spend_usd,
        header: monthLabel,
        meta: { alignRight: true },
        cell: ({ row }) => (
          <span className="inline-flex items-center gap-2">
            {formatUsd(row.original.month.spend_usd)}
            <DataSourceBadge source={row.original.month.source} />
          </span>
        ),
      },
    ],
    [monthLabel, nameHeader, renderName, rowId],
  );

  return (
    <DataTable
      data={rows}
      columns={columns}
      searchPlaceholder={searchPlaceholder}
      emptyMessage={emptyMessage}
      getRowId={rowId}
      initialSorting={[{ id: "month", desc: true }]}
      footer={footer}
    />
  );
}
