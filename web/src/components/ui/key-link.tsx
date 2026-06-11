import { Link } from "react-router-dom";

import { keyDetailPath, resolveKeyLinkTarget } from "../../lib/key-routes";
import { maskKeyId } from "../../lib/format";
import type { APIKey } from "../../types";

type KeyLinkProps = {
  keys?: APIKey[];
  keyValue?: string;
  maskedId?: string;
  scope?: string;
  /** Primary label; falls back to key description or masked id. */
  label?: string;
  showMasked?: boolean;
  className?: string;
};

export default function KeyLink({
  keys,
  keyValue,
  maskedId,
  scope,
  label,
  showMasked = false,
  className = "",
}: KeyLinkProps) {
  const target = resolveKeyLinkTarget(keys, { keyValue, maskedId, scope });
  const record = target && keys ? keys.find((k) => k.key === target) : undefined;
  const masked = maskedId ?? (target ? maskKeyId(target) : undefined);
  const primary = label ?? record?.description ?? masked ?? target ?? "—";

  if (!target) {
    return (
      <span className={className}>
        <span>{primary}</span>
        {showMasked && masked && primary !== masked ? (
          <span className="mt-0.5 block font-mono text-xs text-base-content/50">{masked}</span>
        ) : null}
      </span>
    );
  }

  return (
    <Link to={keyDetailPath(target)} className={`link link-hover link-primary no-underline ${className}`.trim()}>
      <span className="font-medium">{primary}</span>
      {showMasked && masked ? (
        <span className="mt-0.5 block font-mono text-xs opacity-70">{masked}</span>
      ) : null}
    </Link>
  );
}
