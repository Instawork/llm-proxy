import { FormEvent, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";

import { RequestKeyTabIcon } from "../ui/key-tab-icons";
import { useToast } from "../ui/toast";
import { dailyCostLimitFormDollars } from "../../lib/format";
import { keyDetailPath } from "../../lib/key-routes";
import { useCreateKeyRequest, useMe, useMyKeyRequests } from "../../hooks/queries";
import type { KeyRequestRecord, Provider } from "../../types";

const PROVIDERS: Provider[] = ["openai", "anthropic", "gemini", "bedrock"];

function formatTime(value?: string): string {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

function StatusBadge({ status }: { status: KeyRequestRecord["status"] }) {
  const cls =
    status === "pending"
      ? "badge badge-warning"
      : status === "approved"
        ? "badge badge-success"
        : "badge badge-error";
  return <span className={cls}>{status}</span>;
}

type RequestKeyModalProps = {
  open: boolean;
  onClose: () => void;
};

export default function RequestKeyModal({ open, onClose }: RequestKeyModalProps) {
  const { data: me } = useMe();
  const { data: myRequests = [], isLoading } = useMyKeyRequests();
  const createRequest = useCreateKeyRequest();
  const toast = useToast();

  const editorMaxCents = me?.editor_limits?.max_daily_cost_limit_cents ?? 0;
  const editorMaxDollars = editorMaxCents > 0 ? editorMaxCents / 100 : 100;

  const [provider, setProvider] = useState<Provider>("openai");
  const [description, setDescription] = useState("");
  const [dailyCostLimitDollars, setDailyCostLimitDollars] = useState(
    String(editorMaxDollars),
  );

  useEffect(() => {
    if (editorMaxCents > 0) {
      setDailyCostLimitDollars(String(editorMaxCents / 100));
    }
  }, [editorMaxCents]);

  const pendingCount = useMemo(
    () => myRequests.filter((r) => r.status === "pending").length,
    [myRequests],
  );

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const dailyCents = Math.round(Number(dailyCostLimitDollars) * 100);
    try {
      await createRequest.mutateAsync({
        provider,
        description: description.trim(),
        daily_cost_limit: Number.isFinite(dailyCents) && dailyCents > 0 ? dailyCents : undefined,
      });
      toast.push("Key request submitted", "success");
      setDescription("");
      onClose();
    } catch (err) {
      toast.push(err instanceof Error ? err.message : "Failed to submit request", "error");
    }
  };

  if (!open) return null;

  return (
    <dialog className="modal modal-open">
      <div className="modal-box max-w-2xl overflow-hidden p-0">
        <div className="relative border-b border-base-300/60 bg-base-200/30 px-6 py-5">
          <button
            type="button"
            className="btn btn-circle btn-ghost btn-sm absolute right-4 top-4"
            aria-label="Close"
            onClick={onClose}
          >
            x
          </button>
          <div className="flex items-start gap-4 pr-10">
            <div className="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
              <RequestKeyTabIcon className="h-5 w-5" />
            </div>
            <div>
              <h3 className="text-xl font-semibold">Request a key</h3>
              <p className="mt-2 max-w-xl text-sm leading-relaxed text-base-content/70">
                Tell admins exactly what you are building. They will choose the key name and
                provision the org-wide key for you.
              </p>
            </div>
          </div>
        </div>

        <form className="space-y-6 px-6 py-6" onSubmit={onSubmit}>
          <label className="form-control w-full gap-2 pb-1">
            <span className="label-text font-semibold">Provider</span>
            <select
              className="select select-bordered h-11 w-full"
              value={provider}
              onChange={(e) => setProvider(e.target.value as Provider)}
            >
              {PROVIDERS.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </label>

          <div className="space-y-3 pt-1">
            <label className="form-control w-full gap-2">
              <div className="flex items-center justify-between gap-3">
                <span className="label-text font-semibold">Use case</span>
                <span className="badge badge-warning badge-sm">Be specific</span>
              </div>
              <textarea
                className="textarea textarea-bordered min-h-36 w-full leading-relaxed"
                required
                rows={6}
                placeholder="Example: my-service in production calls OpenAI for document classification from the worker queue. Expected volume is about 10k requests/day. We need an org-wide key because this runs in deployed infrastructure."
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </label>
            <div className="grid gap-2 rounded-xl border border-warning/25 bg-warning/10 p-3 text-xs text-base-content/75 sm:grid-cols-3">
              <div className="flex items-start gap-2 rounded-lg bg-base-100/45 px-3 py-2">
                <span className="badge badge-warning badge-xs mt-0.5 shrink-0">1</span>
                <div>
                  <p className="font-medium text-base-content/80">Where it runs</p>
                  <p className="leading-snug">Service/repo and environment</p>
                </div>
              </div>
              <div className="flex items-start gap-2 rounded-lg bg-base-100/45 px-3 py-2">
                <span className="badge badge-warning badge-xs mt-0.5 shrink-0">2</span>
                <div>
                  <p className="font-medium text-base-content/80">What it does</p>
                  <p className="leading-snug">Workload, provider, volume</p>
                </div>
              </div>
              <div className="flex items-start gap-2 rounded-lg bg-base-100/45 px-3 py-2">
                <span className="badge badge-warning badge-xs mt-0.5 shrink-0">3</span>
                <div>
                  <p className="font-medium text-base-content/80">Constraints</p>
                  <p className="leading-snug">Cost, security, compliance</p>
                </div>
              </div>
            </div>
          </div>

          {me?.role === "editor" ? (
            <label className="form-control w-full gap-2">
              <span className="label-text font-semibold">Daily cost limit (USD)</span>
              <input
                type="number"
                className="input input-bordered h-11 w-full"
                min="0"
                step="0.01"
                value={dailyCostLimitDollars}
                onChange={(e) => setDailyCostLimitDollars(e.target.value)}
              />
              <span className="text-xs text-base-content/60">
                Max {dailyCostLimitFormDollars(editorMaxCents)} per day for editors
              </span>
            </label>
          ) : null}

          <div className="modal-action mt-2 border-t border-base-300/60 px-0 pt-5">
            <button type="button" className="btn btn-ghost" onClick={onClose}>
              Cancel
            </button>
            <button
              type="submit"
              className="btn btn-primary min-w-36"
              disabled={createRequest.isPending || !description.trim()}
            >
              {createRequest.isPending ? (
                <span className="loading loading-spinner loading-sm" />
              ) : null}
              Submit request
            </button>
          </div>
        </form>

        {!isLoading && myRequests.length > 0 ? (
          <div className="border-t border-base-300/60 px-6 py-5">
            <div className="mb-3 flex items-center justify-between gap-2">
              <h4 className="text-sm font-semibold">Your requests</h4>
              {pendingCount > 0 ? (
                <span className="badge badge-warning badge-sm">{pendingCount} pending</span>
              ) : null}
            </div>
            <div className="max-h-40 overflow-y-auto rounded-lg ring-1 ring-base-300/60">
              <table className="table table-xs">
                <thead>
                  <tr>
                    <th>Provider</th>
                    <th>Status</th>
                    <th>Submitted</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {myRequests.map((req) => (
                    <tr key={req.id}>
                      <td>{req.provider}</td>
                      <td>
                        <StatusBadge status={req.status} />
                      </td>
                      <td className="whitespace-nowrap">{formatTime(req.created_at)}</td>
                      <td>
                        {req.created_key ? (
                          <Link
                            to={keyDetailPath(req.created_key)}
                            className="link link-primary text-xs"
                            onClick={onClose}
                          >
                            View
                          </Link>
                        ) : req.rejection_reason ? (
                          <span className="text-xs text-error">{req.rejection_reason}</span>
                        ) : (
                          "—"
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        ) : null}
      </div>
      <form method="dialog" className="modal-backdrop">
        <button type="button" aria-label="Close" onClick={onClose} />
      </form>
    </dialog>
  );
}
