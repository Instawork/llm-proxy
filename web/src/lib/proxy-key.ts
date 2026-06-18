const DEFAULT_PREFIX_BASE = "iw";
const SK_LEAD = "sk-";

export function proxyKeyPrefixes(base = DEFAULT_PREFIX_BASE): string[] {
  return [`${SK_LEAD}${base}-`, `${base}-`, `${base}_`, `${base}:`];
}

export function matchedProxyKeyPrefix(
  key: string,
  base = DEFAULT_PREFIX_BASE,
): string | null {
  for (const prefix of proxyKeyPrefixes(base)) {
    if (key.startsWith(prefix)) return prefix;
  }
  return null;
}

export function isProxyKey(value: string | undefined): value is string {
  return Boolean(value && matchedProxyKeyPrefix(value));
}

export function trimProxyKeyPrefix(key: string, base = DEFAULT_PREFIX_BASE): string {
  const prefix = matchedProxyKeyPrefix(key, base);
  return prefix ? key.slice(prefix.length) : key;
}
