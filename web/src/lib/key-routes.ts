import { maskKeyId } from "./format";
import { isProxyKey, matchedProxyKeyPrefix, trimProxyKeyPrefix } from "./proxy-key";
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

export { isProxyKey };

/** Matches admin API rate-limit scope redaction (last 4 of key body after prefix). */
export function redactedRateLimitScopeForKey(key: string): string {
  const body = trimProxyKeyPrefix(key);
  if (body.length <= 4) {
    return `key:••••${body}`;
  }
  return `key:••••${body.slice(-4)}`;
}

/** Resolve suffix from a redacted scope (`••••6e5d`) to a registered key. */
export function findKeyByScopeSuffix(suffix: string, keys: APIKey[]): APIKey | undefined {
  if (!suffix) return undefined;
  const matches = keys.filter((k) => trimProxyKeyPrefix(k.key).endsWith(suffix));
  return matches.length === 1 ? matches[0] : undefined;
}

/** Resolve a masked dashboard key_id to a registered API key. */
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
  if (matchedProxyKeyPrefix(rest)) return rest;
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
