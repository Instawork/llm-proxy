import { useEffect, useRef, useState } from "react";

export interface HistoryPoint {
  t: number;
  value: number;
}

export const LIVE_HISTORY_MAX_POINTS = 60;

/** Shown under trend charts — sets expectations (Stripe-style declarative copy). */
export const LIVE_TREND_CHART_SUBTITLE =
  "Live sparkline from polling while this tab is open — not historical analytics";

/**
 * Accumulates a scalar value into a rolling client-side time series as it
 * changes across polls. The admin API only exposes point-in-time stats, so
 * trend charts are built by sampling here rather than from server history.
 */
export function useHistory(value: number | undefined, deps: unknown[] = []): HistoryPoint[] {
  const [points, setPoints] = useState<HistoryPoint[]>([]);
  const lastRef = useRef<number | undefined>(undefined);

  useEffect(() => {
    if (value === undefined || Number.isNaN(value)) return;
    if (lastRef.current === value && points.length > 0) return;
    lastRef.current = value;
    setPoints((prev) => {
      const next = [...prev, { t: Date.now(), value }];
      return next.length > LIVE_HISTORY_MAX_POINTS
        ? next.slice(next.length - LIVE_HISTORY_MAX_POINTS)
        : next;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, ...deps]);

  return points;
}

/** Footer caption for trend charts — shows sample count and time span when available. */
export function liveTrendCaption(points: HistoryPoint[]): string {
  if (points.length === 0) {
    return "Waiting for first sample…";
  }
  if (points.length === 1) {
    return "1 sample — new points appear when the metric changes";
  }
  const spanSec = Math.round((points[points.length - 1].t - points[0].t) / 1000);
  const mins = Math.floor(spanSec / 60);
  const secs = spanSec % 60;
  const span = mins > 0 ? `${mins}m ${secs}s` : `${secs}s`;
  return `${points.length} samples over ${span} · new points when the metric changes`;
}
