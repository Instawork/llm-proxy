import type { Provider } from "../types";

const MASKED_ID_SPLIT = /…|\u2026|\.\.\./;

export function parseCredentialHashFromMaskedId(maskedId: string): string | null {
  const trimmed = maskedId.trim();
  if (!trimmed) return null;
  const match = trimmed.match(MASKED_ID_SPLIT);
  if (!match || match.index === undefined) return null;
  const hash = trimmed.slice(match.index + match[0].length);
  return /^[0-9a-f]{8}$/.test(hash) ? hash : null;
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
