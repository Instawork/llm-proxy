import { piiRequestAction } from "../../lib/pii-wire-policy";
import type { PIIRecentEvent } from "../../types";

const TONE_CLASS = {
  success: "badge-success",
  warning: "badge-warning",
  error: "badge-error",
  neutral: "badge-ghost",
} as const;

export function PiiRequestActionBadge({
  outcome,
  entityTotal,
  wirePlaceholders,
}: {
  outcome: PIIRecentEvent["outcome"];
  entityTotal: number;
  wirePlaceholders: boolean;
}) {
  const action = piiRequestAction(outcome, entityTotal, { wirePlaceholders });

  return (
    <div className="max-w-[11rem]">
      <span
        className={`badge badge-sm ${TONE_CLASS[action.tone]}`}
        title={action.detail}
      >
        {action.label}
      </span>
      <p className="mt-1 text-[0.65rem] leading-snug text-base-content/55">{action.detail}</p>
    </div>
  );
}
