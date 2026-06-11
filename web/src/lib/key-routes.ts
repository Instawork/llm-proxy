import { maskKeyId } from "./format";
import type { APIKey } from "../types";

const REDACTED_SCOPE_SUFFIX = /^••••(.+)$/;

export function encodeKeyRouteParam(key: string): string {
  return encodeURIComponent(key);
}

export function decodeKeyRouteParam(param: string): string {
  return decodeURIComponent(param);
}

export function keyDetailPath(key: string): string {
  return `/keys/${encodeKeyRouteParam(key)}`;
}

export function isProxyKey(value: string | undefined): value is string {
  return Boolean(value?.startsWith("iw:"));
}

/** Matches admin API rate-limit scope redaction (last 4 of key body after iw:). */
export function redactedRateLimitScopeForKey(key: string): string {
  const body = key.startsWith("iw:") ? key.slice(3) : key;
  if (body.length <= 4) {
    return `key:••••${body}`;
  }
  return `key:••••${body.slice(-4)}`;
}

/** Resolve suffix from a redacted scope (`••••6e5d`) to a registered key. */
export function findKeyByScopeSuffix(suffix: string, keys: APIKey[]): APIKey | undefined {
  if (!suffix) return undefined;
  const matches = keys.filter((k) => {
    const body = k.key.startsWith("iw:") ? k.key.slice(3) : k.key;
    return body.endsWith(suffix);
  });
  return matches.length === 1 ? matches[0] : undefined;
}

/** Resolve a masked dashboard key_id (iw:abc… ) to a registered API key. */
export function findKeyByMaskedId(maskedId: string, keys: APIKey[]): APIKey | undefined {
  if (!maskedId) return undefined;
  const exact = keys.find((k) => maskKeyId(k.key) === maskedId);
  if (exact) return exact;
  const prefix = maskedId.replace(/\u2026$/, "").replace(/…$/, "");
  if (prefix.length < 4) return undefined;
  const matches = keys.filter((k) => k.key.startsWith(prefix));
  return matches.length === 1 ? matches[0] : undefined;
}

export function keyFromRateLimitScope(scope: string, keys?: APIKey[]): string | undefined {
  if (!scope.startsWith("key:")) return undefined;
  const rest = scope.slice(4);
  if (rest.startsWith("iw:")) return rest;
  const redacted = rest.match(REDACTED_SCOPE_SUFFIX);
  if (redacted) {
    return keys ? findKeyByScopeSuffix(redacted[1], keys)?.key : undefined;
  }
  if (rest.includes("•")) return undefined;
  return rest;
}

export function resolveKeyLinkTarget(
  keys: APIKey[] | undefined,
  opts: { keyValue?: string; maskedId?: string; scope?: string },
): string | undefined {
  if (opts.keyValue && isProxyKey(opts.keyValue)) return opts.keyValue;
  const fromScope = opts.scope ? keyFromRateLimitScope(opts.scope, keys) : undefined;
  if (fromScope && isProxyKey(fromScope)) return fromScope;
  if (opts.maskedId && keys) {
    return findKeyByMaskedId(opts.maskedId, keys)?.key;
  }
  return undefined;
}
