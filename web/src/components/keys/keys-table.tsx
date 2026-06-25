import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import KeyLink from "../ui/key-link";
import DataTable from "../ui/data-table";
import { ProviderBadge, StatusBadge } from "../ui/page-header";
import { formatKeySpendCap } from "../../lib/format";
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
  canDelete?: boolean;
  viewerMode?: boolean;
  sharingKey?: string | null;
  maskKey: (key: string) => string;
  formatRateLimits: (record: APIKey) => string;
}

export default function KeysTable({
  keys,
  onShare,
  onEdit,
  onDelete,
  canDelete = true,
  viewerMode = false,
  sharingKey,
  maskKey,
  formatRateLimits,
}: KeysTableProps) {
  const columns = useMemo<ColumnDef<APIKey, unknown>[]>(() => {
    const cols: ColumnDef<APIKey, unknown>[] = [
      {
        id: "name",
        accessorKey: "description",
        header: "Name",
        cell: ({ row }) => (
          <KeyLink
            keyValue={row.original.key}
            keys={keys}
            label={row.original.description?.trim() || "Unnamed key"}
          />
        ),
      },
      {
        id: "key",
        accessorKey: "key",
        header: "Key",
        cell: ({ row }) => (
          <span className="font-mono text-xs text-base-content/80">{maskKey(row.original.key)}</span>
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
        accessorFn: (row) => formatKeySpendCap(row),
        header: viewerMode ? "Monthly limit" : "Spend cap",
        cell: ({ row }) => formatKeySpendCap(row.original),
      },
    ];

    if (!viewerMode) {
      cols.push(
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
      );
    }


    cols.push(
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
            {canDelete ? (
              <button
                type="button"
                className="btn btn-ghost btn-xs text-error"
                onClick={() => onDelete(row.original)}
              >
                Delete
              </button>
            ) : null}
          </div>
        ),
      },
    );

    return cols;
  }, [keys, maskKey, formatRateLimits, onShare, onEdit, onDelete, canDelete, sharingKey, viewerMode]);

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
