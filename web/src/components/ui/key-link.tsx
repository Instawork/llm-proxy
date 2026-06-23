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
  /** Secondary mono line; defaults to masked id when showMasked is set. */
  secondaryLabel?: string;
  showMasked?: boolean;
  className?: string;
};

export default function KeyLink({
  keys,
  keyValue,
  maskedId,
  scope,
  label,
  secondaryLabel,
  showMasked = false,
  className = "",
}: KeyLinkProps) {
  const target = resolveKeyLinkTarget(keys, { keyValue, maskedId, scope });
  const record = target && keys ? keys.find((k) => k.key === target) : undefined;
  const masked = maskedId ?? (target ? maskKeyId(target) : undefined);
  const primary = label ?? record?.description ?? masked ?? target ?? "—";
  const secondary = secondaryLabel ?? masked;

  if (!target) {
    return (
      <span className={className}>
        <span>{primary}</span>
        {showMasked && secondary && primary !== secondary ? (
          <span className="mt-0.5 block font-mono text-xs text-base-content/50">{secondary}</span>
        ) : null}
      </span>
    );
  }

  return (
    <Link to={keyDetailPath(target)} className={`link link-hover link-primary no-underline ${className}`.trim()}>
      <span className="font-medium">{primary}</span>
      {showMasked && secondary ? (
        <span className="mt-0.5 block font-mono text-xs opacity-70">{secondary}</span>
      ) : null}
    </Link>
  );
}
