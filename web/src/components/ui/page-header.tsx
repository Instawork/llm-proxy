import { useEffect, useState } from "react";

import { relativeTime } from "../../lib/format";

interface PageHeaderProps {
  title: string;
  description?: string;
  actions?: React.ReactNode;
}

export default function PageHeader({ title, description, actions }: PageHeaderProps) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-base-content">{title}</h1>
        {description ? <p className="mt-1 max-w-2xl text-sm text-base-content/60">{description}</p> : null}
      </div>
      {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
    </div>
  );
}

interface StatusBadgeProps {
  active: boolean;
  activeLabel?: string;
  inactiveLabel?: string;
}

export function StatusBadge({
  active,
  activeLabel = "Active",
  inactiveLabel = "Inactive",
}: StatusBadgeProps) {
  return (
    <span className={`badge badge-sm ${active ? "badge-success badge-outline" : "badge-ghost"}`}>
      {active ? activeLabel : inactiveLabel}
    </span>
  );
}

export function ProviderBadge({ provider }: { provider: string }) {
  return <span className="badge badge-sm badge-outline badge-primary">{provider}</span>;
}

interface LiveIndicatorProps {
  /** Epoch ms the data was last updated (e.g. TanStack Query dataUpdatedAt). */
  updatedAt?: number;
  /** Whether a background refetch is in flight. */
  fetching?: boolean;
  /** Manual refresh trigger. */
  onRefresh?: () => void;
}

// LiveIndicator surfaces the auto-refresh state common to LLM dashboards
// (Helicone/Portkey): a pulsing "Live" dot, a relative "updated Xs ago"
// label that ticks on its own, and a manual refresh button.
export function LiveIndicator({ updatedAt, fetching, onRefresh }: LiveIndicatorProps) {
  const [, setTick] = useState(0);

  useEffect(() => {
    const id = window.setInterval(() => setTick((t) => t + 1), 1000);
    return () => window.clearInterval(id);
  }, []);

  return (
    <div className="flex items-center gap-3 text-xs text-base-content/60">
      <span className="inline-flex items-center gap-1.5">
        <span className="relative flex h-2 w-2">
          <span
            className={`absolute inline-flex h-full w-full rounded-full bg-success opacity-75 ${fetching ? "animate-ping" : ""}`}
          />
          <span className="relative inline-flex h-2 w-2 rounded-full bg-success" />
        </span>
        Live
      </span>
      {updatedAt ? <span>updated {relativeTime(updatedAt)}</span> : null}
      {onRefresh ? (
        <button
          type="button"
          className="btn btn-ghost btn-xs gap-1"
          onClick={onRefresh}
          aria-label="Refresh now"
        >
          <svg viewBox="0 0 24 24" className="h-3.5 w-3.5 fill-none stroke-current" strokeWidth="2">
            <path d="M21 12a9 9 0 1 1-2.64-6.36M21 3v6h-6" />
          </svg>
          Refresh
        </button>
      ) : null}
    </div>
  );
}

export function LoadingBlock() {
  return (
    <div className="flex min-h-[12rem] items-center justify-center">
      <span className="loading loading-spinner loading-lg text-primary" />
    </div>
  );
}

export function ErrorAlert({ message }: { message: string }) {
  return (
    <div role="alert" className="alert alert-error">
      <span>{message}</span>
    </div>
  );
}

export function EmptyState({
  message,
  action,
}: {
  message: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex flex-col items-center justify-center gap-4 px-6 py-16 text-center">
      <p className="max-w-sm text-sm text-base-content/60">{message}</p>
      {action}
    </div>
  );
}
