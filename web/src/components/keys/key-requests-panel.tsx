import { useMemo, useState } from "react";

import KeyRequestsTable from "../key-requests/key-requests-table";
import { ErrorAlert, LoadingBlock } from "../ui/page-header";
import { useToast } from "../ui/toast";
import { APIClientError } from "../../client";
import { useKeyRequests, useReviewKeyRequest } from "../../hooks/queries";
import type { KeyRequestRecord } from "../../types";

type StatusFilter = "pending" | "all";

export default function KeyRequestsPanel() {
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("pending");
  const requestsQuery = useKeyRequests(statusFilter === "pending" ? "pending" : undefined);
  const reviewRequest = useReviewKeyRequest();
  const toast = useToast();
  const [busyId, setBusyId] = useState<string | null>(null);
  const [rejectTarget, setRejectTarget] = useState<KeyRequestRecord | null>(null);
  const [rejectReason, setRejectReason] = useState("");

  const forbidden =
    requestsQuery.error instanceof APIClientError && requestsQuery.error.status === 403;

  const pendingCount = useMemo(
    () => (requestsQuery.data ?? []).filter((r) => r.status === "pending").length,
    [requestsQuery.data],
  );

  const onApprove = async (request: KeyRequestRecord) => {
    setBusyId(request.id);
    try {
      await reviewRequest.mutateAsync({ id: request.id, action: "approve" });
      toast.push("Key request approved and key created", "success");
    } catch (err) {
      toast.push(err instanceof Error ? err.message : "Failed to approve request", "error");
    } finally {
      setBusyId(null);
    }
  };

  const onConfirmReject = async () => {
    if (!rejectTarget) return;
    setBusyId(rejectTarget.id);
    try {
      await reviewRequest.mutateAsync({
        id: rejectTarget.id,
        action: "reject",
        rejection_reason: rejectReason.trim() || undefined,
      });
      toast.push("Key request rejected", "success");
      setRejectTarget(null);
      setRejectReason("");
    } catch (err) {
      toast.push(err instanceof Error ? err.message : "Failed to reject request", "error");
    } finally {
      setBusyId(null);
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-base-content/70">
          Review pending requests for org-wide service keys. Check that the use case is specific
          enough before you approve and provision the key.
        </p>
        <select
          className="select select-bordered select-sm"
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value as StatusFilter)}
        >
          <option value="pending">Pending only</option>
          <option value="all">All requests</option>
        </select>
      </div>

      {statusFilter === "pending" && pendingCount > 0 ? (
        <div className="glass-panel border-l-4 border-l-warning px-4 py-3 text-sm">
          {pendingCount} request{pendingCount === 1 ? "" : "s"} awaiting review
        </div>
      ) : null}

      {forbidden ? (
        <ErrorAlert message="You do not have permission to view key requests." />
      ) : requestsQuery.isLoading ? (
        <LoadingBlock />
      ) : requestsQuery.isError ? (
        <ErrorAlert
          message={
            requestsQuery.error instanceof Error
              ? requestsQuery.error.message
              : "Failed to load key requests"
          }
        />
      ) : (
        <KeyRequestsTable
          requests={requestsQuery.data ?? []}
          busyId={busyId}
          onApprove={onApprove}
          onReject={setRejectTarget}
        />
      )}

      {rejectTarget ? (
        <dialog className="modal modal-open">
          <div className="modal-box">
            <h3 className="text-lg font-semibold">Reject key request</h3>
            <p className="mt-2 text-sm text-base-content/70">
              {rejectTarget.requester_email} — {rejectTarget.provider}
            </p>
            <label className="form-control mt-4 w-full">
              <span className="label-text">Reason (optional)</span>
              <textarea
                className="textarea textarea-bordered w-full"
                rows={3}
                value={rejectReason}
                onChange={(e) => setRejectReason(e.target.value)}
                placeholder="e.g. Please use an existing shared key for this service"
              />
            </label>
            <div className="modal-action">
              <button type="button" className="btn btn-ghost" onClick={() => setRejectTarget(null)}>
                Cancel
              </button>
              <button
                type="button"
                className="btn btn-error"
                disabled={reviewRequest.isPending}
                onClick={onConfirmReject}
              >
                Reject
              </button>
            </div>
          </div>
          <form method="dialog" className="modal-backdrop">
            <button type="button" onClick={() => setRejectTarget(null)}>
              close
            </button>
          </form>
        </dialog>
      ) : null}
    </div>
  );
}
