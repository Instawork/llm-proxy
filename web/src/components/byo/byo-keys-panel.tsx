import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import ByoBanButton from "./ban-by-key-button";
import DataTable from "../ui/data-table";
import { MaskedCredentialId } from "../ui/masked-credential-id";
import { ProviderBadge, StatusBadge } from "../ui/page-header";
import { useBYOKeys } from "../../hooks/queries";
import { useByoBanActions } from "../../hooks/use-byo-ban-actions";
import { permissions } from "../../lib/permissions";
import { formatUsd, MASKED_CREDENTIAL_HASH_TITLE } from "../../lib/format";
import type { BYOKeyRecord } from "../../types";

function sourceLabel(source: string): string {
  switch (source) {
    case "pii":
      return "PII";
    case "cost":
      return "Cost";
    case "ban":
      return "Ban list";
    default:
      return source;
  }
}

export default function ByoKeysPanel() {
  const byoBanActions = useByoBanActions();
  const { data: rows = [], isLoading, error } = useBYOKeys();

  const columns = useMemo<ColumnDef<BYOKeyRecord, unknown>[]>(
    () => [
      {
        id: "provider",
        accessorKey: "provider",
        header: "Provider",
        cell: ({ getValue }) => <ProviderBadge provider={getValue<string>()} />,
      },
      {
        id: "masked_id",
        accessorKey: "masked_id",
        header: () => (
          <span className="inline-flex items-center gap-1" title={MASKED_CREDENTIAL_HASH_TITLE}>
            Hashed ID
            <span className="badge badge-ghost badge-xs font-normal normal-case">FNV-1a</span>
          </span>
        ),
        cell: ({ getValue }) => <MaskedCredentialId value={getValue<string>()} />,
      },
      {
        id: "status",
        accessorKey: "banned",
        header: "Status",
        cell: ({ getValue }) => (
          <StatusBadge
            active={!getValue<boolean>()}
            activeLabel="Allowed"
            inactiveLabel="Banned"
          />
        ),
      },
      {
        id: "pii_scans",
        accessorKey: "pii_scans",
        header: "PII scans",
        meta: { alignRight: true },
        cell: ({ getValue }) => getValue<number>().toLocaleString(),
      },
      {
        id: "cost_requests",
        accessorKey: "cost_requests",
        header: "Cost reqs",
        meta: { alignRight: true },
        cell: ({ getValue }) => getValue<number>().toLocaleString(),
      },
      {
        id: "spend_usd",
        accessorKey: "spend_usd",
        header: "Spend",
        meta: { alignRight: true },
        cell: ({ getValue }) => formatUsd(getValue<number>()),
      },
      {
        id: "sources",
        accessorKey: "sources",
        header: "Seen in",
        enableSorting: false,
        cell: ({ getValue }) => (
          <div className="flex flex-wrap gap-1">
            {getValue<string[]>().map((source) => (
              <span key={source} className="badge badge-ghost badge-sm">
                {sourceLabel(source)}
              </span>
            ))}
          </div>
        ),
      },
      {
        id: "actions",
        header: () => <span className="sr-only">Actions</span>,
        enableSorting: false,
        meta: { alignRight: true },
        cell: ({ row }) => (
          <ByoBanButton
            maskedId={row.original.masked_id}
            provider={row.original.provider}
            actions={byoBanActions}
          />
        ),
      },
    ],
    [byoBanActions],
  );

  if (!byoBanActions.canManage) {
    return null;
  }

  if (isLoading) {
    return <div className="text-base-content/60">Loading BYO keys…</div>;
  }
  if (error) {
    return <div className="text-error">Failed to load BYO keys</div>;
  }

  return (
    <div className="space-y-2">
      <p className="text-xs text-base-content/55">{MASKED_CREDENTIAL_HASH_TITLE}</p>
      <DataTable
        data={rows}
        columns={columns}
        searchPlaceholder="Filter BYO keys…"
        emptyMessage="No bring-your-own keys observed yet"
        getRowId={(row) => `${row.provider}:${row.hash}`}
      />
    </div>
  );
}
