import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import DataTable from "../ui/data-table";
import { EmptyState, ErrorAlert, LoadingBlock, ProviderBadge, StatusBadge } from "../ui/page-header";
import { formatCount, formatKeySpendCap, formatUsd } from "../../lib/format";
import { formatExpiresAt, isExpired } from "../../lib/key-form";
import type { ServiceKey } from "../../types";

interface ServiceKeysPanelProps {
  keys: ServiceKey[];
  isLoading: boolean;
  error?: Error | null;
}

/**
 * Read-only list of org-wide service keys for viewers granted visibility. The
 * API withholds the usable credential, so only the masked ID is shown and
 * there are no row actions.
 */
export default function ServiceKeysPanel({ keys, isLoading, error }: ServiceKeysPanelProps) {
  const columns = useMemo<ColumnDef<ServiceKey, unknown>[]>(
    () => [
      {
        id: "name",
        accessorFn: (row) => row.description?.trim() || "Unnamed key",
        header: "Name",
      },
      {
        id: "key",
        accessorFn: (row) => row.masked_key_id ?? row.key,
        header: "Key ID",
        cell: ({ getValue }) => <code className="code-chip font-mono text-xs">{getValue<string>()}</code>,
      },
      {
        id: "provider",
        accessorKey: "provider",
        header: "Provider",
        cell: ({ row }) => <ProviderBadge provider={row.original.provider} />,
      },
      {
        id: "status",
        accessorFn: (row) => (row.enabled ? 1 : 0),
        header: "Status",
        cell: ({ row }) => {
          if (!row.original.enabled) {
            return <StatusBadge active={false} inactiveLabel="Disabled" />;
          }
          if (isExpired(row.original.expires_at)) {
            return <span className="badge badge-sm badge-warning badge-outline">Expired</span>;
          }
          return <StatusBadge active activeLabel="Enabled" />;
        },
      },
      {
        id: "expires_at",
        accessorFn: (row) => formatExpiresAt(row.expires_at),
        header: "Expires",
      },
      {
        id: "spend_cap",
        accessorFn: (row) => formatKeySpendCap(row),
        header: "Spend cap",
      },
      {
        id: "spend_today",
        accessorFn: (row) => row.spend?.today.spend_usd ?? 0,
        header: "Spend today",
        cell: ({ row }) => formatUsd(row.original.spend?.today.spend_usd ?? 0, 2),
      },
      {
        id: "spend_month",
        accessorFn: (row) => row.spend?.month.spend_usd ?? 0,
        header: "Spend this month",
        cell: ({ row }) => formatUsd(row.original.spend?.month.spend_usd ?? 0, 2),
      },
      {
        id: "requests_today",
        accessorFn: (row) => row.spend?.today.requests ?? 0,
        header: "Requests today",
        cell: ({ row }) => formatCount(row.original.spend?.today.requests ?? 0),
      },
    ],
    [],
  );

  return (
    <section className="space-y-3" aria-labelledby="service-keys-heading">
      <div>
        <h2 id="service-keys-heading" className="text-lg font-semibold tracking-tight">
          Service keys
        </h2>
        <p className="mt-1 max-w-2xl text-sm text-base-content/60">
          Org-wide keys shared across teams, with their spend caps and current spend. You have read-only access; to use one, request a key from an admin.
        </p>
      </div>
      <div className="glass-panel overflow-hidden">
        {isLoading ? (
          <LoadingBlock />
        ) : error ? (
          <ErrorAlert message={error.message || "Failed to load service keys"} />
        ) : keys.length === 0 ? (
          <EmptyState message="No service keys yet." />
        ) : (
          <DataTable columns={columns} data={keys} emptyMessage="No service keys yet." />
        )}
      </div>
    </section>
  );
}
