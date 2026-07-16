import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import KeyLink from "../ui/key-link";
import DataTable from "../ui/data-table";
import { SectionPanel, type DataSource } from "../ui/data-source";
import { ProviderBadge } from "../ui/provider-badge";
import type { RangeKey } from "../../lib/daily-history";
import { usageDisplayRows, type UsageDisplayRow, type UsageRow } from "../../lib/group-rows";
import { formatCount } from "../../lib/format";
import { useCollapsedRows } from "../../hooks/use-collapsed-rows";
import type { APIKey } from "../../types";

function rangeLabel(range: RangeKey): string {
  return range === "today" ? "today" : `last ${range === "7d" ? "7" : "30"} days`;
}

interface UsageTableProps {
  title: string;
  rows: UsageRow[];
  keys?: APIKey[];
  linkKeys?: boolean;
  source: DataSource;
  range: RangeKey;
}

export default function UsageTable({ title, rows, keys, linkKeys = false, source, range }: UsageTableProps) {
  const totalTokens = rows.reduce((sum, row) => sum + row.tokens, 0);
  const subtitle =
    source === "redis" ? `Summed Redis rollups · ${rangeLabel(range)}` : `Live memory · ${rangeLabel(range)}`;
  const entityLabel = title.replace("By ", "").toLowerCase() + "s";
  const { displayData, onSearchActiveChange, footer } = useCollapsedRows(rows, usageDisplayRows, entityLabel);

  const labelHeader = title.replace("By ", "");

  const columns = useMemo<ColumnDef<UsageDisplayRow, unknown>[]>(
    () => [
      {
        id: "label",
        accessorKey: "label",
        header: labelHeader,
        cell: ({ row }) => {
          const data = row.original;
          if (data.isOthers) {
            return <span className="italic text-base-content/60">{data.label}</span>;
          }
          if (labelHeader === "Provider") {
            return <ProviderBadge provider={data.label} />;
          }
          return linkKeys ? (
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
        meta: { alignRight: true },
        cell: ({ getValue }) => formatCount(getValue<number>()),
      },
      {
        id: "tokens",
        accessorKey: "tokens",
        header: "Tokens",
        meta: { alignRight: true },
        cell: ({ getValue }) => formatCount(getValue<number>()),
      },
      {
        id: "share",
        accessorFn: (row) => (totalTokens > 0 ? (row.tokens / totalTokens) * 100 : 0),
        header: "Share",
        meta: { alignRight: true },
        cell: ({ row }) => (
          <span className="text-base-content/60">
            {totalTokens > 0 ? `${((row.original.tokens / totalTokens) * 100).toFixed(1)}%` : "—"}
          </span>
        ),
      },
    ],
    [keys, linkKeys, labelHeader, totalTokens],
  );

  return (
    <SectionPanel title={title} subtitle={subtitle} source={source}>
      <DataTable
        data={displayData}
        columns={columns}
        searchPlaceholder={`Filter ${entityLabel}…`}
        emptyMessage="No usage recorded today"
        getRowId={(row) => (row.isOthers ? "__others__" : row.scope)}
        onSearchActiveChange={onSearchActiveChange}
        footer={footer}
      />
    </SectionPanel>
  );
}
