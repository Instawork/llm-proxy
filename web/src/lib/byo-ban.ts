import { parseMaskedCredentialId } from "./format";
import type { Provider } from "../types";

export function parseCredentialHashFromMaskedId(maskedId: string): string | null {
  return parseMaskedCredentialId(maskedId)?.hash ?? null;
}

const BYO_PREFIX_PROVIDERS: [string, Provider][] = [
  ["sk-ant-", "anthropic"],
  ["sk-proj-", "openai"],
  ["sk-svcacct-", "openai"],
  ["sk-or-", "openai"],
  ["sk-", "openai"],
  ["AIza", "gemini"],
];

export function inferProviderFromMaskedId(maskedId: string): Provider | null {
  const head = maskedId.split(/…|\u2026|\.\.\./)[0] ?? "";
  for (const [prefix, provider] of BYO_PREFIX_PROVIDERS) {
    if (head.startsWith(prefix)) return provider;
  }
  return null;
}

export function byoBanLookupKey(provider: string, maskedId: string): string | null {
  const hash = parseCredentialHashFromMaskedId(maskedId);
  if (!hash) return null;
  return `${provider}:${hash}`;
}
