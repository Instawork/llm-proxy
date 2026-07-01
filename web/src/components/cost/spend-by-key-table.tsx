import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import KeyLink from "../ui/key-link";
import ByoBanButton from "../byo/ban-by-key-button";
import DataTable from "../ui/data-table";
import type { CostKeyAgg } from "../../lib/daily-history";
import { spendByKeyDisplayRows, type SpendByKeyDisplayRow } from "../../lib/group-rows";
import { formatCount, formatUsd } from "../../lib/format";
import { inferProviderFromMaskedId } from "../../lib/byo-ban";
import { useCollapsedRows } from "../../hooks/use-collapsed-rows";
import type { ByoBanActions } from "../../hooks/use-byo-ban-actions";
import type { APIKey } from "../../types";

interface SpendByKeyTableProps {
  rows: CostKeyAgg[];
  keys: APIKey[];
  byoBanActions: ByoBanActions;
}

export default function SpendByKeyTable({ rows, keys, byoBanActions }: SpendByKeyTableProps) {
  const { displayData, onSearchActiveChange, footer } = useCollapsedRows(
    rows,
    spendByKeyDisplayRows,
    "keys",
  );

  const columns = useMemo<ColumnDef<SpendByKeyDisplayRow, unknown>[]>(
    () => [
      {
        id: "key",
        accessorKey: "key_id",
        header: "Key",
        cell: ({ row }) => {
          const data = row.original;
          if (data.isOthers) {
            return <span className="italic text-base-content/60">{data.key_id}</span>;
          }
          const inferredProvider = inferProviderFromMaskedId(data.key_id);
          return data.key_id ? (
            <div className="flex items-center gap-2">
              <KeyLink keys={keys} maskedId={data.key_id} showMasked className="text-xs" />
              {inferredProvider ? (
                <ByoBanButton
                  maskedId={data.key_id}
                  provider={inferredProvider}
                  actions={byoBanActions}
                />
              ) : null}
            </div>
          ) : (
            "—"
          );
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
        id: "input",
        accessorFn: (row) => row.input_spend_usd ?? 0,
        header: "Input",
        meta: { alignRight: true },
        cell: ({ getValue }) => formatUsd(getValue<number>()),
      },
      {
        id: "output",
        accessorFn: (row) => row.output_spend_usd ?? 0,
        header: "Output",
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
      {
        id: "tokens",
        accessorFn: (row) => `${row.input_tokens}/${row.output_tokens}`,
        header: "Tokens",
        meta: { alignRight: true },
        cell: ({ row }) => (
          <span className="text-base-content/70">
            {formatCount(row.original.input_tokens)}/{formatCount(row.original.output_tokens)}
          </span>
        ),
      },
    ],
    [keys, byoBanActions],
  );

  return (
    <DataTable
      data={displayData}
      columns={columns}
      searchPlaceholder="Filter keys…"
      emptyMessage="No spend recorded for this window"
      getRowId={(row) => (row.isOthers ? "__others__" : row.key_id || String(row.requests))}
      onSearchActiveChange={onSearchActiveChange}
      footer={footer}
      initialSorting={[{ id: "total", desc: true }]}
    />
  );
}
