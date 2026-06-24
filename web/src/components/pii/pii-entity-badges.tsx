import { PII_POLICY_HINTS, piiEntityPolicy } from "../../lib/pii-wire-policy";

const POLICY_BADGE: Record<ReturnType<typeof piiEntityPolicy>, string> = {
  mask: "badge-info",
  seal: "badge-warning",
  redact: "badge-error",
};

export function PiiEntityBadges({
  entityCounts,
  outcome,
}: {
  entityCounts?: Record<string, number> | null;
  outcome: string;
}) {
  const entries = Object.entries(entityCounts ?? {});
  if (entries.length === 0) {
    if (outcome === "ok") {
      return <span className="text-base-content/40">none detected</span>;
    }
    return <span className="text-base-content/40">not scanned</span>;
  }

  return (
    <div className="flex flex-wrap gap-1">
      {entries.map(([name, n]) => {
        const policy = piiEntityPolicy(name);
        return (
          <span
            key={name}
            className={`badge badge-sm badge-outline gap-1 ${POLICY_BADGE[policy]}`}
            title={PII_POLICY_HINTS[policy]}
          >
            <span>{name.replaceAll("_", " ")}</span>
            <span className="opacity-60">×{n}</span>
            <span className="text-[0.6rem] uppercase tracking-wide opacity-70">{policy}</span>
          </span>
        );
      })}
    </div>
  );
}
