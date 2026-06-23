import { parseCredentialHashFromMaskedId } from "../../lib/byo-ban";
import { isByoMaskedKeyId } from "../../lib/pii-key-display";
import type { ByoBanActions } from "../../hooks/use-byo-ban-actions";
import type { Provider } from "../../types";

interface ByoBanButtonProps {
  maskedId: string;
  provider: string;
  compact?: boolean;
  actions: ByoBanActions;
}

export default function ByoBanButton({
  maskedId,
  provider,
  compact = true,
  actions,
}: ByoBanButtonProps) {
  const hash = parseCredentialHashFromMaskedId(maskedId);

  if (!actions.canManage || !isByoMaskedKeyId(maskedId) || !hash || !provider) {
    return null;
  }

  const existing = actions.findBan(provider, hash);

  if (existing) {
    return (
      <button
        type="button"
        className={compact ? "btn btn-ghost btn-xs text-success" : "btn btn-outline btn-sm"}
        disabled={actions.pending}
        onClick={() => actions.unban(existing.provider, existing.hash)}
      >
        {actions.pending ? <span className="loading loading-spinner loading-xs" /> : "Unban"}
      </button>
    );
  }

  return (
    <button
      type="button"
      className={compact ? "btn btn-ghost btn-xs text-error" : "btn btn-outline btn-sm text-error"}
      disabled={actions.pending}
      onClick={() => actions.ban(provider as Provider, maskedId)}
    >
      {actions.pending ? <span className="loading loading-spinner loading-xs" /> : "Ban BYO"}
    </button>
  );
}
