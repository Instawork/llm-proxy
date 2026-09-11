import { formatCount } from "../../lib/format";
import { UNMETERED_HELP, unmeteredEndpointSummary } from "../../lib/spend-overview";
import type { UnmeteredStats } from "../../types";

/** Endpoint-by-endpoint breakdown of today's unmetered calls; wrap in a section/panel. */
export function UnmeteredEndpointList({ stats }: { stats: UnmeteredStats }) {
  if (stats.requests === 0) {
    return <p className="p-5 text-sm text-base-content/60">No unmetered calls today.</p>;
  }
  return (
    <div className="space-y-3 p-5">
      <p className="text-sm text-base-content/70">
        <span className="font-semibold text-base-content">{formatCount(stats.requests)}</span> unmetered
        {stats.requests === 1 ? " call" : " calls"} today
      </p>
      <ul className="divide-y divide-base-300/60 text-sm">
        {stats.endpoints.map((row) => (
          <li key={row.endpoint} className="flex items-center justify-between gap-4 py-1.5">
            <code className="truncate text-xs">{row.endpoint}</code>
            <span className="shrink-0 tabular-nums">{formatCount(row.requests)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** One-line note for key cards and tables; renders nothing when there is nothing to explain. */
export function UnmeteredNote({ stats, className = "" }: { stats: UnmeteredStats; className?: string }) {
  if (stats.requests === 0) return null;
  const summary = unmeteredEndpointSummary(stats, 5);
  return (
    <p className={`truncate text-xs text-base-content/60 ${className}`} title={`${summary}\n\n${UNMETERED_HELP}`}>
      {formatCount(stats.requests)} unmetered · <code>{summary}</code>
    </p>
  );
}
