import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Link } from "react-router-dom";

import DataTable from "../ui/data-table";
import { ProviderBadge } from "../ui/page-header";
import { keyDetailPathForMaskedId } from "../../lib/key-routes";
import { canResetClaim } from "../../lib/key-request-claim";
import {
  formatRequestLimit,
  keyRequestSourceLabel,
  keyRequestStatusBadgeClass,
  safeTicketUrl,
} from "../../lib/key-request-meta";
import type { KeyRequestRecord } from "../../types";

function formatTime(value?: string): string {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

function StatusBadge({ status }: { status: KeyRequestRecord["status"] }) {
  return <span className={keyRequestStatusBadgeClass(status)}>{status}</span>;
}

function SourceCell({ request }: { request: KeyRequestRecord }) {
  const ticketUrl = safeTicketUrl(request.external_ticket_url);
  return (
    <div className="flex flex-col items-start gap-1">
      <span className="badge badge-outline badge-sm">
        {keyRequestSourceLabel(request.source)}
      </span>
      {request.external_ticket_id ? (
        ticketUrl ? (
          <a
            href={ticketUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="link link-primary font-mono text-xs"
          >
            {request.external_ticket_id}
          </a>
        ) : (
          <span className="font-mono text-xs">{request.external_ticket_id}</span>
        )
      ) : null}
    </div>
  );
}

interface KeyRequestsTableProps {
  requests: KeyRequestRecord[];
  busyId?: string | null;
  onApprove: (request: KeyRequestRecord) => void;
  onReject: (request: KeyRequestRecord) => void;
  onResetClaim: (request: KeyRequestRecord) => void;
}

export default function KeyRequestsTable({
  requests,
  busyId,
  onApprove,
  onReject,
  onResetClaim,
}: KeyRequestsTableProps) {
  const columns = useMemo<ColumnDef<KeyRequestRecord, unknown>[]>(
    () => [
      {
        id: "requester_email",
        accessorKey: "requester_email",
        header: "Requester",
      },
      {
        id: "source",
        accessorKey: "source",
        header: "Source",
        cell: ({ row }) => <SourceCell request={row.original} />,
      },
      {
        id: "provider",
        accessorKey: "provider",
        header: "Provider",
        cell: ({ getValue }) => <ProviderBadge provider={getValue<string>()} />,
      },
      {
        id: "name",
        accessorKey: "name",
        header: "Name",
        cell: ({ getValue }) => (
          <span className="font-mono text-sm">{getValue<string>()}</span>
        ),
      },
      {
        id: "description",
        accessorKey: "description",
        header: "Description",
        cell: ({ row }) => (
          <div className="flex max-w-xs flex-col gap-1">
            <span className="whitespace-pre-wrap break-words">
              {row.original.description}
            </span>
            {row.original.justification ? (
              <details className="text-xs">
                <summary className="cursor-pointer text-base-content/70">
                  Justification
                </summary>
                <p className="whitespace-pre-wrap break-words pt-1">
                  {row.original.justification}
                </p>
              </details>
            ) : null}
          </div>
        ),
      },
      {
        id: "daily_cost_limit",
        accessorKey: "daily_cost_limit",
        header: "Daily limit",
        cell: ({ getValue }) => formatRequestLimit(getValue<number>()),
      },
      {
        id: "monthly_cost_limit",
        accessorKey: "monthly_cost_limit",
        header: "Monthly limit",
        cell: ({ getValue }) => formatRequestLimit(getValue<number>()),
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
        id: "created_key_id",
        accessorKey: "created_key_id",
        header: "Key",
        cell: ({ row }) =>
          row.original.created_key_id ? (
            <div className="flex flex-col items-start gap-1">
              <Link
                to={keyDetailPathForMaskedId(row.original.created_key_id)}
                className="link link-primary"
              >
                View
              </Link>
              <span className="text-xs text-base-content/60">
                {row.original.claimable ? "Not claimed yet" : "Claimed"}
              </span>
            </div>
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
          const busy = busyId === req.id;
          if (canResetClaim(req)) {
            return (
              <div className="flex justify-end">
                <button
                  type="button"
                  className="btn btn-ghost btn-xs"
                  disabled={busy}
                  title="Let the requester reveal the key one more time"
                  onClick={() => onResetClaim(req)}
                >
                  Reset claim
                </button>
              </div>
            );
          }
          if (req.status !== "pending") return null;
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
    [busyId, onApprove, onReject, onResetClaim],
  );

  return (
    <DataTable columns={columns} data={requests} emptyMessage="No key requests yet" />
  );
}
