import { useMemo } from "react";
import { Link } from "react-router-dom";
import type { ColumnDef } from "@tanstack/react-table";

import KeyLink from "../ui/key-link";
import { MaskedKey } from "../ui/masked-key";
import DataTable from "../ui/data-table";
import { ProviderBadge, StatusBadge } from "../ui/page-header";
import { formatKeySpendCap } from "../../lib/format";
import { keySetupPath } from "../../lib/key-routes";
import type { APIKey, PiiRedactSetting, Provider } from "../../types";

function piiLabel(value?: PiiRedactSetting): string {
  if (value === true) return "On";
  if (value === false) return "Off";
  return "Inherit";
}

function formatCreatedAt(value?: string): string {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

interface KeysTableProps {
  keys: APIKey[];
  onShare: (record: APIKey) => void;
  onEdit: (record: APIKey) => void;
  onDelete: (record: APIKey) => void;
  canDelete?: boolean;
  viewerMode?: boolean;
  sharingKey?: string | null;
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
        header: viewerMode ? "Proxy key" : "Key",
        cell: ({ row }) => <MaskedKey value={row.original.key} />,
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
        id: "created_at",
        accessorKey: "created_at",
        header: "Created",
        cell: ({ getValue }) => (
          <span className="whitespace-nowrap text-xs text-base-content/70">
            {formatCreatedAt(getValue<string>())}
          </span>
        ),
      },
      {
        id: "actions",
        header: () => <span className="sr-only">Actions</span>,
        enableSorting: false,
        meta: { alignRight: true },
        cell: ({ row }) => (
          <div className="flex justify-end gap-2">
            {viewerMode ? (
              <Link to={keySetupPath(row.original.key)} className="btn btn-ghost btn-xs">
                How to use
              </Link>
            ) : (
              <>
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
              </>
            )}
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
  }, [keys, formatRateLimits, onShare, onEdit, onDelete, canDelete, sharingKey, viewerMode]);

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
