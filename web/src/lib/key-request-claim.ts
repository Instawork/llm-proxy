import { APIClientError } from "../client";
import type { KeyRequestRecord } from "../types";

/** Dashboard path (under the router basename) that opens the claim dialog. */
export function claimKeyPath(requestId: string): string {
  return `/keys?claim=${encodeURIComponent(requestId)}`;
}

/** Whether an admin can let the requester reveal the key one more time. */
export function canResetClaim(req: KeyRequestRecord): boolean {
  return req.status === "approved" && !req.claimable && Boolean(req.claimed_at);
}

export function claimErrorMessage(err: unknown): string {
  if (err instanceof APIClientError) {
    switch (err.status) {
      case 404:
        return "This key request was not found, or it was filed by someone else.";
      case 409:
        return "This key request has not been approved yet.";
      case 410:
        return "This key was already claimed. Ask an admin to reset the claim if you lost it.";
      case 403:
        return "The claim was blocked because it did not come from the dashboard.";
    }
  }
  return err instanceof Error ? err.message : "Failed to claim key";
}
