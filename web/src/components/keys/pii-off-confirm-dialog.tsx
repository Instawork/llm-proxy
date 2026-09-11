import { useState } from "react";

import type { Provider } from "../../types";
import { ProviderLabel } from "../ui/page-header";

export const PII_OFF_CONFIRM_PHRASE = "OFF";

export function needsPiiOffConfirmation(provider: Provider, redactPii: boolean | null): boolean {
  return redactPii === false && provider !== "bedrock";
}

// Two-step confirmation before saving a non-Bedrock key with PII redaction
// off: an explanation the admin must continue past, then a typed phrase.
export default function PiiOffConfirmDialog({
  provider,
  pending,
  onCancel,
  onConfirm,
}: {
  provider: Provider;
  pending: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const [step, setStep] = useState<1 | 2>(1);
  const [phrase, setPhrase] = useState("");
  const phraseMatches = phrase.trim().toUpperCase() === PII_OFF_CONFIRM_PHRASE;

  return (
    <dialog className="modal modal-open" open>
      <div className="modal-box">
        <h3 className="text-lg font-semibold">
          {step === 1 ? "Turn off PII redaction?" : "Confirm: send PII unredacted"}
        </h3>
        {step === 1 ? (
          <p className="py-4 text-sm text-base-content/70">
            Every request on this key will reach <ProviderLabel provider={provider} /> with names,
            emails, phone numbers and other personal data exactly as sent. Nothing is masked and
            nothing is restored.
          </p>
        ) : (
          <div className="space-y-3 py-4">
            <p className="text-sm text-base-content/70">
              This change is logged against your account. Type{" "}
              <span className="code-chip font-mono">{PII_OFF_CONFIRM_PHRASE}</span> to confirm.
            </p>
            <input
              type="text"
              className="input input-bordered w-full font-mono"
              autoFocus
              autoComplete="off"
              value={phrase}
              placeholder={PII_OFF_CONFIRM_PHRASE}
              onChange={(event) => setPhrase(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter" && phraseMatches && !pending) {
                  event.preventDefault();
                  onConfirm();
                }
              }}
            />
          </div>
        )}
        <div className="modal-action">
          <button type="button" className="btn btn-ghost" onClick={onCancel}>
            Cancel
          </button>
          {step === 1 ? (
            <button type="button" className="btn btn-warning" onClick={() => setStep(2)}>
              Continue
            </button>
          ) : (
            <button
              type="button"
              className="btn btn-error"
              disabled={!phraseMatches || pending}
              onClick={onConfirm}
            >
              {pending ? <span className="loading loading-spinner loading-sm" /> : null}
              Turn off PII redaction
            </button>
          )}
        </div>
      </div>
      <form method="dialog" className="modal-backdrop">
        <button type="button" aria-label="Close" onClick={onCancel} />
      </form>
    </dialog>
  );
}
