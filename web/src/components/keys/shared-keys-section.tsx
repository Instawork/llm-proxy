import { Link } from "react-router-dom";

import { MaskedCredentialId } from "../ui/masked-credential-id";
import { ProviderBadge, StatusBadge } from "../ui/page-header";
import { formatDailyCostLimit, formatMonthlyCostLimit, formatUsd } from "../../lib/format";
import { keyDetailPathForMaskedId } from "../../lib/key-routes";
import type { APIKey } from "../../types";

export function sharedKeyCapLabel(key: APIKey): string {
  const cap = key.spend?.cap;
  if (cap && cap.cents > 0) {
    return cap.period === "monthly"
      ? formatMonthlyCostLimit(cap.cents)
      : formatDailyCostLimit(cap.cents);
  }
  if (key.monthly_cost_limit && key.monthly_cost_limit > 0) {
    return formatMonthlyCostLimit(key.monthly_cost_limit);
  }
  return formatDailyCostLimit(key.daily_cost_limit);
}

/**
 * Org-wide keys another user has granted this viewer access to. Rows are
 * read-only: the response carries a masked ID and cost data, never a usable key.
 */
export default function SharedKeysSection({ keys }: { keys: APIKey[] }) {
  if (keys.length === 0) {
    return null;
  }
  return (
    <section className="space-y-3" aria-labelledby="shared-keys-heading">
      <div>
        <h2 id="shared-keys-heading" className="text-lg font-semibold">
          Shared with me
        </h2>
        <p className="text-sm text-base-content/60">
          Read-only view of service keys you have been given access to. You can see their spend
          and limits, but not the key itself.
        </p>
      </div>
      <div className="glass-panel overflow-x-auto">
        <table className="table table-sm">
          <thead>
            <tr>
              <th>Name</th>
              <th>Key ID</th>
              <th>Provider</th>
              <th>Status</th>
              <th className="text-right">Spend today</th>
              <th className="text-right">Spend this month</th>
              <th className="text-right">Limit</th>
            </tr>
          </thead>
          <tbody>
            {keys.map((key) => (
              <tr key={key.masked_key_id ?? key.key}>
                <td>
                  <Link
                    to={keyDetailPathForMaskedId(key.masked_key_id ?? key.key)}
                    className="link link-hover link-primary font-medium no-underline"
                  >
                    {key.description?.trim() || "Unnamed key"}
                  </Link>
                  <span className="badge badge-ghost badge-xs ml-2">Read-only</span>
                </td>
                <td>
                  <MaskedCredentialId value={key.masked_key_id ?? key.key} />
                </td>
                <td>
                  <ProviderBadge provider={key.provider} />
                </td>
                <td>
                  <StatusBadge
                    active={key.enabled}
                    activeLabel="Enabled"
                    inactiveLabel="Disabled"
                  />
                </td>
                <td className="text-right">{formatUsd(key.spend?.today.spend_usd)}</td>
                <td className="text-right">{formatUsd(key.spend?.month.spend_usd)}</td>
                <td className="text-right">{sharedKeyCapLabel(key)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
