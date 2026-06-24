import type { PIIRecentEvent } from "../types";

export type PiiEntityPolicy = "mask" | "seal" | "redact";

const ENTITY_POLICY: Record<string, PiiEntityPolicy> = {
  US_SSN: "seal",
  US_ITIN: "seal",
  US_PASSPORT: "seal",
  DATE_OF_BIRTH: "seal",
  US_STREET_ADDRESS: "seal",
  CREDIT_CARD: "redact",
  US_BANK_NUMBER: "redact",
  IBAN_CODE: "redact",
  US_DRIVER_LICENSE: "mask",
  PERSON: "mask",
  EMAIL_ADDRESS: "mask",
  PHONE_NUMBER: "mask",
  LOCATION: "mask",
  IP_ADDRESS: "mask",
};

export const PII_POLICY_HINTS: Record<PiiEntityPolicy, string> = {
  mask: "Placeholder sent to the LLM; original restored in the client response",
  seal: "Placeholder sent to the LLM; stays opaque to the client",
  redact: "[REDACTED] marker sent upstream; never restored",
};

export function piiEntityPolicy(entityType: string): PiiEntityPolicy {
  return ENTITY_POLICY[entityType] ?? "redact";
}

export type PiiRequestAction = {
  label: string;
  detail: string;
  tone: "success" | "warning" | "error" | "neutral";
};

export function piiRequestAction(
  outcome: PIIRecentEvent["outcome"],
  entityTotal: number,
  opts: { wirePlaceholders: boolean },
): PiiRequestAction {
  switch (outcome) {
    case "ok":
      if (entityTotal > 0 && opts.wirePlaceholders) {
        return {
          label: "Forwarded",
          detail: "PII scrubbed before upstream; request not blocked",
          tone: "success",
        };
      }
      if (entityTotal > 0) {
        return {
          label: "Forwarded",
          detail: "PII detected for logging only — upstream saw the raw body",
          tone: "warning",
        };
      }
      return {
        label: "Forwarded",
        detail: "No PII detected",
        tone: "neutral",
      };
    case "fail_closed":
      return {
        label: "Blocked",
        detail: "503 — request stopped because redaction failed",
        tone: "error",
      };
    case "fail_open":
      return {
        label: "Forwarded",
        detail: "Unredacted — redaction failed but request was allowed through",
        tone: "warning",
      };
    case "oversize":
      return {
        label: "Forwarded",
        detail: "Body over max size — scan skipped, raw body proxied",
        tone: "warning",
      };
    default:
      return { label: outcome, detail: "", tone: "neutral" };
  }
}

export function piiPipelineSummary(opts: {
  enabled: boolean;
  wirePlaceholders: boolean;
  failMode: string;
}): string {
  if (!opts.enabled) {
    return "PII redaction is disabled — requests pass through unchanged.";
  }
  const fail =
    opts.failMode === "closed"
      ? "Redaction errors return 503 (request blocked)."
      : "Redaction errors fail open (unredacted body may reach the LLM).";
  const wire = opts.wirePlaceholders
    ? "Detected PII is replaced with placeholders before the upstream LLM sees the body; requests are not blocked on detection alone."
    : "Observability-only mode: scans are logged but the upstream LLM still receives the raw body.";
  return `${wire} ${fail}`;
}
