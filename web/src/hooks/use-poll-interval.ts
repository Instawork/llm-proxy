import { useEffect, useState } from "react";

/** Poll interval while the tab is visible (matches TanStack Query live hooks). */
export const LIVE_POLL_MS = 5000;

/** Poll interval when the tab is in the background — cuts API noise without stopping updates. */
export const BACKGROUND_POLL_MS = 60_000;

function intervalForVisibility(visibleMs: number, hiddenMs: number): number {
  if (typeof document === "undefined") return visibleMs;
  return document.visibilityState === "visible" ? visibleMs : hiddenMs;
}

/**
 * Returns a poll interval that slows when document.visibilityState is hidden.
 * Inspired by TanStack Query's refetchIntervalInBackground guidance — we still
 * refetch in background, just less aggressively.
 */
export function usePollInterval(
  visibleMs = LIVE_POLL_MS,
  hiddenMs = BACKGROUND_POLL_MS,
): number {
  const [interval, setInterval] = useState(() => intervalForVisibility(visibleMs, hiddenMs));

  useEffect(() => {
    const onVisibilityChange = () => {
      setInterval(intervalForVisibility(visibleMs, hiddenMs));
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => document.removeEventListener("visibilitychange", onVisibilityChange);
  }, [visibleMs, hiddenMs]);

  return interval;
}
