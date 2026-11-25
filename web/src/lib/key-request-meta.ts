import type { KeyRequestStatus } from "../types";

/** Display label for where a key request came from. */
export function keyRequestSourceLabel(source?: string): string {
  return source ? source : "dashboard";
}

/**
 * Returns the ticket URL only when it is an absolute https URL, so a
 * caller-supplied value can never render as a javascript: or http: link.
 */
export function safeTicketUrl(url?: string): string | undefined {
  if (!url) return undefined;
  try {
    return new URL(url).protocol === "https:" ? url : undefined;
  } catch {
    return undefined;
  }
}

export function keyRequestStatusBadgeClass(status: KeyRequestStatus): string {
  switch (status) {
    case "pending":
    case "approving":
      return "badge badge-warning";
    case "approved":
      return "badge badge-success";
    case "cancelled":
      return "badge badge-ghost";
    default:
      return "badge badge-error";
  }
}

/** Formats a requested cost limit in cents; 0 or unset renders as a dash. */
export function formatRequestLimit(cents?: number): string {
  return cents ? `$${(cents / 100).toFixed(2)}` : "—";
}
