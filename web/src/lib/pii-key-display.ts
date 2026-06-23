import { maskKeyId } from "./format";
import { findKeyByMaskedId } from "./key-routes";
import { matchedProxyKeyPrefix, trimProxyKeyPrefix } from "./proxy-key";
import type { APIKey } from "../types";

const MASKED_ID_SPLIT = /…|\u2026/;

const BYO_PREFIX_LABELS: [string, string][] = [
  ["sk-ant-", "Anthropic BYO"],
  ["sk-proj-", "OpenAI project BYO"],
  ["sk-svcacct-", "OpenAI service BYO"],
  ["sk-or-", "OpenRouter BYO"],
  ["sk-", "OpenAI BYO"],
  ["AIza", "Google BYO"],
  ["gsk_", "Groq BYO"],
  ["xai-", "xAI BYO"],
];

export function maskedIdHead(maskedId: string): string {
  const idx = maskedId.search(MASKED_ID_SPLIT);
  return idx >= 0 ? maskedId.slice(0, idx) : maskedId;
}

export function isProxyMaskedKeyId(maskedId: string): boolean {
  const head = maskedIdHead(maskedId);
  if (!head) return false;
  return matchedProxyKeyPrefix(head) !== null || head.startsWith("sk-iw") || head.startsWith("iw:");
}

export function isByoMaskedKeyId(maskedId: string): boolean {
  if (!maskedId || !MASKED_ID_SPLIT.test(maskedId)) return false;
  return !isProxyMaskedKeyId(maskedId);
}

function byoFamilyLabel(maskedId: string): string {
  const head = maskedIdHead(maskedId);
  for (const [prefix, label] of BYO_PREFIX_LABELS) {
    if (head.startsWith(prefix)) return label;
  }
  return head || maskedId;
}

export function piiKeyPrimaryLabel(maskedId: string, keys: APIKey[]): string {
  if (!maskedId) return "—";

  const linked = findKeyByMaskedId(maskedId, keys);
  if (linked?.description?.trim()) return linked.description.trim();
  if (linked) {
    const body = trimProxyKeyPrefix(linked.key);
    return body.length <= 8 ? body : `${body.slice(0, 8)}…`;
  }

  if (isProxyMaskedKeyId(maskedId)) {
    return "Proxy key";
  }

  return byoFamilyLabel(maskedId);
}

export function piiKeySecondaryLabel(maskedId: string, keys: APIKey[]): string | undefined {
  if (!maskedId) return undefined;

  const linked = findKeyByMaskedId(maskedId, keys);
  if (linked) return maskKeyId(linked.key);

  if (isProxyMaskedKeyId(maskedId)) {
    return "sk-iw…";
  }

  if (isByoMaskedKeyId(maskedId)) {
    return maskedId;
  }

  return maskedId;
}

export function piiKeyShowSecondary(maskedId: string, keys: APIKey[]): boolean {
  if (!maskedId) return false;
  if (findKeyByMaskedId(maskedId, keys)) return true;
  return isByoMaskedKeyId(maskedId) || isProxyMaskedKeyId(maskedId);
}
