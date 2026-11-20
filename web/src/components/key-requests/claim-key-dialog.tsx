import { useState } from "react";
import { Link } from "react-router-dom";

import { ProxyKeyUsagePanel } from "../keys/proxy-key-usage-panel";
import { CopyButton } from "../ui/copy-button";
import { useClaimKeyRequest } from "../../hooks/queries";
import { claimErrorMessage } from "../../lib/key-request-claim";
import { keyDetailPathForMaskedId } from "../../lib/key-routes";
import type { ClaimKeyResponse } from "../../types";

type ClaimKeyDialogProps = {
  requestId: string;
  onClose: () => void;
};

/**
 * One-time reveal of an approved key request's key. The key is held only in
 * this component's state: it is never cached, stored, or put in a URL, and it
 * is gone once the dialog closes.
 */
export default function ClaimKeyDialog({ requestId, onClose }: ClaimKeyDialogProps) {
  const claim = useClaimKeyRequest();
  const [claimed, setClaimed] = useState<ClaimKeyResponse | null>(null);
  const [error, setError] = useState<string | null>(null);

  const onReveal = async () => {
    setError(null);
    try {
      setClaimed(await claim.mutateAsync(requestId));
    } catch (err) {
      setError(claimErrorMessage(err));
    } finally {
      claim.reset();
    }
  };

  const close = () => {
    setClaimed(null);
    onClose();
  };

  return (
    <dialog className="modal modal-open">
      <div className="modal-box max-w-3xl">
        <h3 className="text-lg font-semibold">Claim your API key</h3>

        {claimed ? (
          <div className="mt-4 space-y-4">
            <div role="alert" className="alert alert-warning text-sm">
              This key is shown only once. Copy it now and store it in a secret manager. If you
              lose it, an admin has to reset the claim before you can see it again.
            </div>
            <div className="flex items-center gap-2 rounded-lg bg-base-200 p-3">
              <code className="flex-1 break-all font-mono text-sm">{claimed.key}</code>
              <CopyButton value={claimed.key} label="Copy key" />
            </div>
            {claimed.base_url ? (
              <ProxyKeyUsagePanel
                provider={claimed.provider}
                baseUrl={claimed.base_url}
                proxyKey={claimed.key}
                embedded
              />
            ) : null}
            <Link
              to={keyDetailPathForMaskedId(claimed.masked_key_id)}
              className="link link-primary text-sm"
              onClick={close}
            >
              View key details
            </Link>
          </div>
        ) : (
          <div className="mt-4 space-y-4">
            <p className="text-sm text-base-content/70">
              Your key request was approved. The key is shown only once, so have somewhere safe to
              store it before you reveal it.
            </p>
            {error ? (
              <div role="alert" className="alert alert-error text-sm">
                {error}
              </div>
            ) : null}
          </div>
        )}

        <div className="modal-action">
          <button type="button" className="btn btn-ghost" onClick={close}>
            {claimed ? "Done" : "Cancel"}
          </button>
          {claimed ? null : (
            <button
              type="button"
              className="btn btn-primary"
              disabled={claim.isPending}
              onClick={onReveal}
            >
              {claim.isPending ? <span className="loading loading-spinner loading-sm" /> : null}
              Reveal key
            </button>
          )}
        </div>
      </div>
      <form method="dialog" className="modal-backdrop">
        <button type="button" aria-label="Close" onClick={close} />
      </form>
    </dialog>
  );
}
