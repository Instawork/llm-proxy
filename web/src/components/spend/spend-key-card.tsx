import { Link } from "react-router-dom";

import { DataSourceBadge } from "../ui/data-source";
import { MaskedCredentialId } from "../ui/masked-credential-id";
import { ProviderBadge } from "../ui/page-header";
import { SpendLimitProgress } from "../ui/spend-limit-progress";
import { UnmeteredNote } from "./unmetered-calls";
import { formatCount, formatUsd } from "../../lib/format";
import { keyDetailPathForMaskedId } from "../../lib/key-routes";
import { capFraction } from "../../lib/spend-overview";
import type { SpendKeyRow } from "../../types";

export default function SpendKeyCard({
  row,
  monthLabel,
  scopeNote,
}: {
  row: SpendKeyRow;
  monthLabel: string;
  /** Set when this key's spend is not part of the totals for the selected view. */
  scopeNote?: string | null;
}) {
  const cap = capFraction(row);
  return (
    <Link
      to={keyDetailPathForMaskedId(row.key_id)}
      className={`glass-panel block p-5 transition hover:border-primary/40 ${row.enabled ? "" : "opacity-60"}`}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="truncate font-medium">{row.description || row.key_id}</p>
          <MaskedCredentialId value={row.key_id} className="text-base-content/50" />
        </div>
        <ProviderBadge provider={row.provider} />
      </div>
      {row.owner_email ? <p className="mt-1 truncate text-xs text-base-content/60">{row.owner_email}</p> : null}
      {row.shared || !row.enabled ? (
        <div className="mt-2 flex flex-wrap gap-1.5">
          {row.shared ? <span className="badge badge-info badge-outline badge-sm">Shared</span> : null}
          {!row.enabled ? <span className="badge badge-ghost badge-sm">Disabled</span> : null}
        </div>
      ) : null}

      <div className="mt-4 grid grid-cols-2 gap-4">
        <div>
          <div className="flex items-center gap-2">
            <p className="text-xs text-base-content/50">Today</p>
            <DataSourceBadge source={row.today.source} />
          </div>
          <p className="text-2xl font-semibold tracking-tight">{formatUsd(row.today.spend_usd)}</p>
          <p className="text-xs text-base-content/50">{formatCount(row.today.requests)} requests</p>
        </div>
        <div>
          <div className="flex items-center gap-2">
            <p className="text-xs text-base-content/50">{monthLabel}</p>
            <DataSourceBadge source={row.month.source} />
          </div>
          <p className="text-2xl font-semibold tracking-tight">{formatUsd(row.month.spend_usd)}</p>
        </div>
      </div>

      <UnmeteredNote stats={row.unmetered} className="mt-3" />
      {scopeNote ? <p className="mt-3 text-xs text-base-content/50">{scopeNote}</p> : null}
      {cap ? <SpendLimitProgress spentUsd={cap.spentUsd} limitCents={cap.limitCents} label={cap.label} /> : null}
    </Link>
  );
}
