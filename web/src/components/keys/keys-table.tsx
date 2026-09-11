import { useMemo } from "react";
import { Link } from "react-router-dom";
import type { ColumnDef } from "@tanstack/react-table";

import KeyLink from "../ui/key-link";
import { MaskedKey } from "../ui/masked-key";
import DataTable from "../ui/data-table";
import { ProviderBadge, StatusBadge } from "../ui/page-header";
import { ProviderIcon } from "../ui/provider-badge";
import { formatKeySpendCap } from "../../lib/format";
import { formatExpiresAt, isExpired } from "../../lib/key-form";
import { keySetupPath } from "../../lib/key-routes";
import { keyDisplayRows, uniformOr, type KeyRow } from "../../lib/key-stacks";
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

/** Stacked provider icons for a collapsed row; falls back to a single badge for a leaf. */
function ProviderIcons({ providers }: { providers: Provider[] }) {
  return (
    <span className="inline-flex items-center gap-1" title={providers.join(", ")}>
      {providers.map((provider) => (
        <ProviderIcon key={provider} provider={provider} size={14} />
      ))}
    </span>
  );
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
  // Viewers only see their own keys, so stacking would collapse the whole page into one row
  // and hide Reveal/Copy/"How to use" behind a click; keep their rows flat.
  const rows = useMemo(() => keyDisplayRows(keys, { stack: !viewerMode }), [keys, viewerMode]);

  const columns = useMemo<ColumnDef<KeyRow, unknown>[]>(() => {
    const cols: ColumnDef<KeyRow, unknown>[] = [
      {
        id: "name",
        accessorFn: (row) => (row.kind === "stack" ? row.name : row.key.description?.trim() || "Unnamed key"),
        header: "Name",
        cell: ({ row }) => {
          const original = row.original;
          if (original.kind === "stack") {
            return (
              <button
                type="button"
                className="btn btn-ghost btn-xs gap-1.5 px-1"
                aria-expanded={row.getIsExpanded()}
                onClick={row.getToggleExpandedHandler()}
              >
                <span aria-hidden>{row.getIsExpanded() ? "▾" : "▸"}</span>
                <span className="font-medium">{original.name}</span>
                <span className="badge badge-ghost badge-xs">{original.keys.length}</span>
              </button>
            );
          }
          return (
            <span className={row.depth > 0 ? "pl-6" : undefined}>
              <KeyLink
                keyValue={original.key.key}
                keys={keys}
                label={original.key.description?.trim() || "Unnamed key"}
              />
            </span>
          );
        },
      },
      {
        id: "key",
        accessorFn: (row) => (row.kind === "stack" ? "" : row.key.key),
        header: viewerMode ? "Proxy key" : "Key",
        cell: ({ row }) => {
          const original = row.original;
          if (original.kind === "stack") {
            return (
              <span className="text-xs text-base-content/50">
                {original.keys.length} keys
              </span>
            );
          }
          return <MaskedKey value={original.key.key} />;
        },
      },
      {
        id: "provider",
        accessorFn: (row) => (row.kind === "stack" ? row.providers.join(" ") : row.key.provider),
        header: "Provider",
        cell: ({ row }) => {
          const original = row.original;
          return original.kind === "stack" ? (
            <ProviderIcons providers={original.providers} />
          ) : (
            <ProviderBadge provider={original.key.provider} />
          );
        },
      },
      {
        id: "status",
        accessorFn: (row) => (row.kind === "stack" ? row.enabledCount / row.keys.length : row.key.enabled ? 1 : 0),
        header: "Status",
        cell: ({ row }) => {
          const original = row.original;
          if (original.kind === "stack") {
            const total = original.keys.length;
            if (original.enabledCount === total) {
              return <StatusBadge active activeLabel="Enabled" />;
            }
            if (original.enabledCount === 0) {
              return <StatusBadge active={false} inactiveLabel="Disabled" />;
            }
            return (
              <span className="badge badge-sm badge-warning badge-outline">
                {original.enabledCount} of {total} enabled
              </span>
            );
          }
          if (!original.key.enabled) {
            return <StatusBadge active={false} inactiveLabel="Disabled" />;
          }
          if (isExpired(original.key.expires_at)) {
            return <span className="badge badge-sm badge-warning badge-outline">Expired</span>;
          }
          return <StatusBadge active activeLabel="Enabled" />;
        },
      },
      {
        id: "expires_at",
        accessorFn: (row) => (row.kind === "stack" ? uniformOr(row.keys, (k) => formatExpiresAt(k.expires_at), "Mixed") : formatExpiresAt(row.key.expires_at)),
        header: "Expires",
        cell: ({ row, getValue }) => {
          const original = row.original;
          const warn = original.kind === "key" && isExpired(original.key.expires_at);
          return (
            <span className={`whitespace-nowrap text-xs ${warn ? "text-warning" : "text-base-content/70"}`}>
              {getValue<string>()}
            </span>
          );
        },
      },
      {
        id: "costLimit",
        accessorFn: (row) => (row.kind === "stack" ? uniformOr(row.keys, formatKeySpendCap, "Mixed") : formatKeySpendCap(row.key)),
        header: viewerMode ? "Monthly limit" : "Spend cap",
        cell: ({ row, getValue }) => {
          const original = row.original;
          const value = getValue<string>();
          if (value !== "Mixed" || original.kind !== "stack") return value;
          return (
            <span className="italic text-base-content/60" title={original.keys.map(formatKeySpendCap).join(" · ")}>
              {value}
            </span>
          );
        },
      },
    ];

    if (!viewerMode) {
      cols.push(
        {
          id: "rateLimits",
          accessorFn: (row) => (row.kind === "stack" ? uniformOr(row.keys, formatRateLimits, "Mixed") : formatRateLimits(row.key)),
          header: "Rate limits",
          cell: ({ getValue }) => (
            <span className="max-w-[10rem] truncate text-xs text-base-content/70" title={getValue<string>()}>
              {getValue<string>()}
            </span>
          ),
        },
        {
          id: "pii",
          accessorFn: (row) =>
            row.kind === "stack"
              ? uniformOr(row.keys, (k) => piiLabel(k.redact_pii), "Mixed")
              : piiLabel(row.key.redact_pii),
          header: "PII redact",
          cell: ({ getValue }) => (
            <span className="badge badge-ghost badge-sm">{getValue<string>()}</span>
          ),
        },
      );
    }

    cols.push(
      {
        id: "created_at",
        accessorFn: (row) => (row.kind === "stack" ? row.created_at : row.key.created_at),
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
        cell: ({ row }) => {
          const original = row.original;
          // Actions are per key; a stack row exposes none, only its expanded leaves do.
          if (original.kind === "stack") return null;
          const record = original.key;
          return (
            <div className="flex justify-end gap-2">
              {viewerMode ? (
                <Link to={keySetupPath(record.key)} className="btn btn-ghost btn-xs">
                  How to use
                </Link>
              ) : (
                <>
                  <button
                    type="button"
                    className="btn btn-ghost btn-xs"
                    disabled={sharingKey === record.key}
                    onClick={() => onShare(record)}
                  >
                    {sharingKey === record.key ? (
                      <span className="loading loading-spinner loading-xs" />
                    ) : (
                      "Share"
                    )}
                  </button>
                  <button type="button" className="btn btn-ghost btn-xs" onClick={() => onEdit(record)}>
                    Edit
                  </button>
                </>
              )}
              {canDelete ? (
                <button
                  type="button"
                  className="btn btn-ghost btn-xs text-error"
                  onClick={() => onDelete(record)}
                >
                  Delete
                </button>
              ) : null}
            </div>
          );
        },
      },
    );

    return cols;
  }, [keys, formatRateLimits, onShare, onEdit, onDelete, canDelete, sharingKey, viewerMode]);

  return (
    <DataTable
      data={rows}
      columns={columns}
      getRowId={(row) => row.id}
      getSubRows={(row) => (row.kind === "stack" ? row.subRows : undefined)}
      searchPlaceholder="Filter keys…"
      emptyMessage="No API keys"
    />
  );
}
