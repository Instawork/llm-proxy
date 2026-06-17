import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import KeyLink from "../ui/key-link";
import DataTable from "../ui/data-table";
import { ProviderBadge, StatusBadge } from "../ui/page-header";
import { formatDailyCostLimit } from "../../lib/format";
import type { APIKey, PiiRedactSetting, Provider } from "../../types";

function piiLabel(value?: PiiRedactSetting): string {
  if (value === true) return "On";
  if (value === false) return "Off";
  return "Inherit";
}

interface KeysTableProps {
  keys: APIKey[];
  onShare: (record: APIKey) => void;
  onEdit: (record: APIKey) => void;
  onDelete: (record: APIKey) => void;
  sharingKey?: string | null;
  maskKey: (key: string) => string;
  formatRateLimits: (record: APIKey) => string;
}

export default function KeysTable({
  keys,
  onShare,
  onEdit,
  onDelete,
  sharingKey,
  maskKey,
  formatRateLimits,
}: KeysTableProps) {
  const columns = useMemo<ColumnDef<APIKey, unknown>[]>(
    () => [
      {
        id: "key",
        accessorKey: "key",
        header: "Key",
        cell: ({ row }) => (
          <KeyLink keyValue={row.original.key} keys={keys} label={maskKey(row.original.key)} />
        ),
      },
      {
        id: "provider",
        accessorKey: "provider",
        header: "Provider",
        cell: ({ getValue }) => <ProviderBadge provider={getValue<Provider>()} />,
      },
      {
        id: "status",
        accessorKey: "enabled",
        header: "Status",
        cell: ({ getValue }) => (
          <StatusBadge active={getValue<boolean>()} activeLabel="Enabled" inactiveLabel="Disabled" />
        ),
      },
      {
        id: "costLimit",
        accessorKey: "daily_cost_limit",
        header: "Cost limit",
        cell: ({ getValue }) => formatDailyCostLimit(getValue<number>()),
      },
      {
        id: "rateLimits",
        accessorFn: (row) => formatRateLimits(row),
        header: "Rate limits",
        cell: ({ getValue }) => (
          <span className="max-w-[10rem] truncate text-xs text-base-content/70" title={getValue<string>()}>
            {getValue<string>()}
          </span>
        ),
      },
      {
        id: "pii",
        accessorKey: "redact_pii",
        header: "PII redact",
        cell: ({ getValue }) => (
          <span className="badge badge-ghost badge-sm">{piiLabel(getValue<PiiRedactSetting>())}</span>
        ),
      },
      {
        id: "description",
        accessorKey: "description",
        header: "Description",
        cell: ({ row }) =>
          row.original.description ? (
            <KeyLink keyValue={row.original.key} keys={keys} label={row.original.description} />
          ) : (
            <span className="max-w-xs truncate text-base-content/70">—</span>
          ),
      },
      {
        id: "actions",
        header: () => <span className="sr-only">Actions</span>,
        enableSorting: false,
        meta: { alignRight: true },
        cell: ({ row }) => (
          <div className="flex justify-end gap-2">
            <button
              type="button"
              className="btn btn-ghost btn-xs"
              disabled={sharingKey === row.original.key}
              onClick={() => onShare(row.original)}
            >
              {sharingKey === row.original.key ? (
                <span className="loading loading-spinner loading-xs" />
              ) : (
                "Share"
              )}
            </button>
            <button type="button" className="btn btn-ghost btn-xs" onClick={() => onEdit(row.original)}>
              Edit
            </button>
            <button
              type="button"
              className="btn btn-ghost btn-xs text-error"
              onClick={() => onDelete(row.original)}
            >
              Delete
            </button>
          </div>
        ),
      },
    ],
    [keys, maskKey, formatRateLimits, onShare, onEdit, onDelete, sharingKey],
  );

  return (
    <DataTable
      data={keys}
      columns={columns}
      searchPlaceholder="Filter keys…"
      emptyMessage="No API keys"
      getRowId={(row) => row.key}
    />
  );
}
