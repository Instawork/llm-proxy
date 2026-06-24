import {
  MASKED_CREDENTIAL_HASH_TITLE,
  parseMaskedCredentialId,
} from "../../lib/format";

type MaskedCredentialIdProps = {
  value: string;
  className?: string;
  /** Show a short "hash" label before the digest (default true). */
  showHashLabel?: boolean;
};

export function MaskedCredentialId({
  value,
  className = "",
  showHashLabel = true,
}: MaskedCredentialIdProps) {
  const parsed = parseMaskedCredentialId(value);
  if (!parsed) {
    return <span className={`font-mono text-xs ${className}`.trim()}>{value}</span>;
  }

  return (
    <span
      className={`inline-flex flex-wrap items-baseline gap-x-0.5 font-mono text-xs ${className}`.trim()}
      title={MASKED_CREDENTIAL_HASH_TITLE}
    >
      <span>{parsed.prefix}</span>
      <span className="text-base-content/35">…</span>
      {showHashLabel ? (
        <span className="text-[0.65rem] font-sans uppercase tracking-wide text-base-content/45">
          hash
        </span>
      ) : null}
      <span className="rounded bg-base-200 px-1 font-mono text-[0.7rem] text-base-content/75">
        {parsed.hash}
      </span>
    </span>
  );
}
