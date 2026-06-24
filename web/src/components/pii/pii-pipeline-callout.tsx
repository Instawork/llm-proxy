import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import KeyLink from "../ui/key-link";
import ByoBanButton from "../byo/ban-by-key-button";
import DataTable from "../ui/data-table";
import { ProviderBadge } from "../ui/page-header";
import { PiiRequestActionBadge } from "../pii/pii-request-action";
import { PiiEntityBadges } from "../pii/pii-entity-badges";
import type { ByoBanActions } from "../../hooks/use-byo-ban-actions";
import {
  piiKeyPrimaryLabel,
  piiKeySecondaryLabel,
  piiKeyShowSecondary,
} from "../../lib/pii-key-display";
import { inferProviderFromMaskedId } from "../../lib/byo-ban";
import { piiPipelineSummary } from "../../lib/pii-wire-policy";
import type { APIKey, IDGateRecentEvent, PIIRecentEvent } from "../../types";

const ENTITY_POLICY_GROUPS = [
  {
    label: "MASK",
    tone: "badge-info",
    summary: "Temporary placeholder sent upstream; original value can be restored in the client response.",
    examples: "person, location, email, phone, driver license",
  },
  {
    label: "SEAL",
    tone: "badge-warning",
    summary: "Placeholder sent upstream and kept opaque in the client response.",
    examples: "SSN, passport, date of birth, street address",
  },
  {
    label: "REDACT",
    tone: "badge-error",
    summary: "[REDACTED] marker sent upstream and never restored.",
    examples: "credit card, bank account, unknown sensitive entity",
  },
];

const FIELD_GUIDE = [
  ["Fail mode", "What happens if the text redaction service errors: open forwards the request, closed blocks it."],
  ["Requests scanned", "Requests whose text body was evaluated by the redaction middleware."],
  ["With PII", "Scanned requests where Presidio detected at least one configured entity."],
  ["Entities redacted", "Total entity hits replaced, sealed, or redacted across scanned requests."],
  ["Recent detections", "Request-level audit metadata: provider, key, entities, and forwarding/blocking action."],
  ["ID gate events", "Image OCR scans for embedded government ID documents; text-only IDs stay in text redaction."],
];

function idGateOutcomeBadge(outcome: IDGateRecentEvent["outcome"]) {
  const map: Record<IDGateRecentEvent["outcome"], string> = {
    clear: "badge-success",
    blocked: "badge-error",
    fail_open: "badge-warning",
    fail_closed: "badge-error",
  };
  const labels: Record<IDGateRecentEvent["outcome"], string> = {
    clear: "Cleared",
    blocked: "Blocked 422",
    fail_open: "Fail open",
    fail_closed: "Fail closed",
  };
  return <span className={`badge badge-sm ${map[outcome]}`}>{labels[outcome]}</span>;
}

function StatusBadge({ enabled }: { enabled: boolean }) {
  return (
    <span className={`badge badge-sm ${enabled ? "badge-success badge-outline" : "badge-neutral"}`}>
      {enabled ? "Enabled" : "Disabled"}
    </span>
  );
}

export function PiiPipelineCallout({
  piiEnabled,
  wirePlaceholders,
  piiFailMode,
  idGateEnabled,
  idGateFailMode,
}: {
  piiEnabled: boolean;
  wirePlaceholders: boolean;
  piiFailMode: string;
  idGateEnabled: boolean;
  idGateFailMode: string;
}) {
  return (
    <details className="group rounded-2xl border border-info/20 bg-info/5 text-sm shadow-sm">
      <summary className="flex cursor-pointer list-none flex-col gap-3 p-4 marker:hidden sm:flex-row sm:items-center sm:justify-between">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-base font-semibold text-base-content">PII Guidelines</h2>
            <StatusBadge enabled={piiEnabled} />
            <span className="badge badge-info badge-outline">Presidio pipeline</span>
          </div>
          <p className="text-xs text-base-content/60">
            Metadata-only view of text redaction and embedded image ID checks. Raw PII is not stored here.
          </p>
        </div>
        <span className="inline-flex items-center gap-2 text-xs font-medium text-info">
          <span className="group-open:hidden">Show field guide</span>
          <span className="hidden group-open:inline">Hide field guide</span>
          <svg
            viewBox="0 0 20 20"
            className="h-4 w-4 transition-transform group-open:rotate-180"
            fill="currentColor"
            aria-hidden="true"
          >
            <path
              fillRule="evenodd"
              d="M5.23 7.21a.75.75 0 0 1 1.06.02L10 11.17l3.71-3.94a.75.75 0 1 1 1.08 1.04l-4.25 4.5a.75.75 0 0 1-1.08 0l-4.25-4.5a.75.75 0 0 1 .02-1.06Z"
              clipRule="evenodd"
            />
          </svg>
        </span>
      </summary>

      <div className="grid gap-3 border-t border-info/10 p-4 pt-3 lg:grid-cols-2">
        <div className="rounded-xl border border-base-300/70 bg-base-100 p-4">
          <div className="mb-2 flex items-center justify-between gap-3">
            <h3 className="font-semibold">Text redaction</h3>
            <StatusBadge enabled={piiEnabled} />
          </div>
          <p className="text-base-content/70">
            {piiPipelineSummary({ enabled: piiEnabled, wirePlaceholders, failMode: piiFailMode })}
          </p>
          <dl className="mt-3 grid gap-2 text-xs sm:grid-cols-2">
            <div>
              <dt className="font-medium text-base-content">Wire mode</dt>
              <dd className="text-base-content/60">
                {wirePlaceholders ? "Placeholders sent to the LLM" : "Detection-only; raw body sent upstream"}
              </dd>
            </div>
            <div>
              <dt className="font-medium text-base-content">Failure behavior</dt>
              <dd className="capitalize text-base-content/60">Fail {piiFailMode}</dd>
            </div>
          </dl>
        </div>

        <div className="rounded-xl border border-base-300/70 bg-base-100 p-4">
          <div className="mb-2 flex items-center justify-between gap-3">
            <h3 className="font-semibold">ID gate</h3>
            <StatusBadge enabled={idGateEnabled} />
          </div>
          <p className="text-base-content/70">
            {idGateEnabled
              ? "OCR scans embedded images for US passports and driver licenses. Detected document images are blocked with HTTP 422."
              : "Embedded government ID images are not OCR-scanned."}
          </p>
          <dl className="mt-3 grid gap-2 text-xs sm:grid-cols-2">
            <div>
              <dt className="font-medium text-base-content">Covers</dt>
              <dd className="text-base-content/60">Images attached to chat requests</dd>
            </div>
            <div>
              <dt className="font-medium text-base-content">Failure behavior</dt>
              <dd className="capitalize text-base-content/60">Fail {idGateFailMode}</dd>
            </div>
          </dl>
        </div>
      </div>

      <div className="grid gap-3 px-4 pb-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
        <div className="rounded-xl border border-base-300/70 bg-base-100 p-4">
          <h3 className="mb-3 font-semibold">Entity handling</h3>
          <div className="space-y-3">
            {ENTITY_POLICY_GROUPS.map((group) => (
              <div key={group.label} className="flex gap-3">
                <span className={`badge badge-sm shrink-0 ${group.tone}`}>{group.label}</span>
                <div>
                  <p className="text-base-content/70">{group.summary}</p>
                  <p className="text-xs text-base-content/50">{group.examples}</p>
                </div>
              </div>
            ))}
          </div>
        </div>

        <div className="rounded-xl border border-base-300/70 bg-base-100 p-4">
          <h3 className="mb-3 font-semibold">Field guide</h3>
          <dl className="grid gap-x-4 gap-y-3 sm:grid-cols-2">
            {FIELD_GUIDE.map(([label, description]) => (
              <div key={label}>
                <dt className="font-medium text-base-content">{label}</dt>
                <dd className="text-xs leading-5 text-base-content/60">{description}</dd>
              </div>
            ))}
          </dl>
        </div>
      </div>
    </details>
  );
}

export function IdGateRecentTable({
  rows,
  keys,
  byoBanActions,
}: {
  rows: IDGateRecentEvent[];
  keys: APIKey[];
  byoBanActions: ByoBanActions;
}) {
  const columns = useMemo<ColumnDef<IDGateRecentEvent, unknown>[]>(
    () => [
      {
        id: "time",
        accessorFn: (row) => new Date(row.time * 1000).toLocaleTimeString(),
        header: "Time",
        cell: ({ getValue }) => <span className="whitespace-nowrap text-base-content/70">{getValue<string>()}</span>,
      },
      {
        id: "provider",
        accessorKey: "provider",
        header: "Provider",
        cell: ({ getValue }) => <ProviderBadge provider={getValue<string>()} />,
      },
      {
        id: "key",
        accessorKey: "key_id",
        header: "Key",
        cell: ({ row }) => {
          const keyId = row.original.key_id;
          if (!keyId) return "—";
          return (
            <div className="flex items-center gap-2">
              <KeyLink
                keys={keys}
                maskedId={keyId}
                label={piiKeyPrimaryLabel(keyId, keys)}
                secondaryLabel={piiKeyShowSecondary(keyId, keys) ? piiKeySecondaryLabel(keyId, keys) : undefined}
                showMasked={piiKeyShowSecondary(keyId, keys)}
                className="font-mono text-xs"
              />
              <ByoBanButton maskedId={keyId} provider={row.original.provider} actions={byoBanActions} />
            </div>
          );
        },
      },
      {
        id: "images",
        accessorKey: "image_count",
        header: "Images",
        meta: { alignRight: true },
        cell: ({ getValue }) => getValue<number>() ?? "—",
      },
      {
        id: "entity",
        accessorKey: "entity_type",
        header: "Gov ID",
        cell: ({ row }) =>
          row.original.entity_type ? (
            <span className="badge badge-sm badge-outline">{row.original.entity_type.replaceAll("_", " ")}</span>
          ) : (
            <span className="text-base-content/40">—</span>
          ),
      },
      {
        id: "outcome",
        accessorKey: "outcome",
        header: "Action",
        cell: ({ getValue }) => idGateOutcomeBadge(getValue<IDGateRecentEvent["outcome"]>()),
      },
      {
        id: "latency",
        accessorKey: "duration_ms",
        header: "Latency",
        cell: ({ getValue }) => <span className="text-base-content/70">{getValue<number>().toFixed(1)} ms</span>,
      },
    ],
    [keys, byoBanActions],
  );

  return (
    <DataTable
      data={rows}
      columns={columns}
      searchPlaceholder="Filter ID gate events…"
      emptyMessage="No embedded-image scans yet (text-only requests do not appear here)"
      getRowId={(row, index) => `${row.time}-${index}`}
    />
  );
}
