import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Link } from "react-router-dom";

import DataTable from "../ui/data-table";
import { keyDetailPath } from "../../lib/key-routes";
import type { KeyRequestRecord } from "../../types";

function formatTime(value?: string): string {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

function StatusBadge({ status }: { status: KeyRequestRecord["status"] }) {
  const cls =
    status === "pending"
      ? "badge badge-warning"
      : status === "approved"
        ? "badge badge-success"
        : "badge badge-error";
  return <span className={cls}>{status}</span>;
}

interface KeyRequestsTableProps {
  requests: KeyRequestRecord[];
  busyId?: string | null;
  onApprove: (request: KeyRequestRecord) => void;
  onReject: (request: KeyRequestRecord) => void;
}

export default function KeyRequestsTable({
  requests,
  busyId,
  onApprove,
  onReject,
}: KeyRequestsTableProps) {
  const columns = useMemo<ColumnDef<KeyRequestRecord, unknown>[]>(
    () => [
      {
        id: "requester_email",
        accessorKey: "requester_email",
        header: "Requester",
      },
      {
        id: "provider",
        accessorKey: "provider",
        header: "Provider",
      },
      {
        id: "description",
        accessorKey: "description",
        header: "Description",
        cell: ({ getValue }) => (
          <span className="max-w-xs truncate">{getValue<string>()}</span>
        ),
      },
      {
        id: "daily_cost_limit",
        accessorKey: "daily_cost_limit",
        header: "Daily limit",
        cell: ({ getValue }) => {
          const cents = getValue<number>();
          return cents ? `$${(cents / 100).toFixed(2)}` : "—";
        },
      },
      {
        id: "status",
        accessorKey: "status",
        header: "Status",
        cell: ({ getValue }) => <StatusBadge status={getValue<KeyRequestRecord["status"]>()} />,
      },
      {
        id: "created_at",
        accessorKey: "created_at",
        header: "Submitted",
        cell: ({ getValue }) => formatTime(getValue<string>()),
      },
      {
        id: "created_key",
        accessorKey: "created_key",
        header: "Key",
        cell: ({ row }) =>
          row.original.created_key ? (
            <Link
              to={keyDetailPath(row.original.created_key!)}
              className="link link-primary"
            >
              View
            </Link>
          ) : row.original.rejection_reason ? (
            <span className="text-sm text-error">{row.original.rejection_reason}</span>
          ) : (
            "—"
          ),
      },
      {
        id: "actions",
        header: "",
        cell: ({ row }) => {
          const req = row.original;
          if (req.status !== "pending") return null;
          const busy = busyId === req.id;
          return (
            <div className="flex justify-end gap-2">
              <button
                type="button"
                className="btn btn-primary btn-xs"
                disabled={busy}
                onClick={() => onApprove(req)}
              >
                {busy ? <span className="loading loading-spinner loading-xs" /> : null}
                Approve
              </button>
              <button
                type="button"
                className="btn btn-ghost btn-xs text-error"
                disabled={busy}
                onClick={() => onReject(req)}
              >
                Reject
              </button>
            </div>
          );
        },
      },
    ],
    [busyId, onApprove, onReject],
  );

  return (
    <DataTable columns={columns} data={requests} emptyMessage="No key requests yet" />
  );
}
