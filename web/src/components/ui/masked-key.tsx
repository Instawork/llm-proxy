import { useState } from "react";

import { CopyButton } from "./copy-button";

export function maskKey(key: string): string {
  if (key.length <= 12) return "••••";
  return `${key.slice(0, 7)}${"•".repeat(18)}${key.slice(-4)}`;
}

export function MaskedKey({
  value,
  className = "font-mono text-xs",
}: {
  value: string;
  className?: string;
}) {
  const [revealed, setRevealed] = useState(false);

  return (
    <div className="flex min-w-0 items-center gap-1.5">
      <code
        className={`code-chip max-w-[12rem] truncate ${className}`}
        title={revealed ? value : "Click Reveal to show full key"}
      >
        {revealed ? value : maskKey(value)}
      </code>
      <button
        type="button"
        className="btn btn-ghost btn-xs shrink-0"
        onClick={() => setRevealed((v) => !v)}
      >
        {revealed ? "Hide" : "Reveal"}
      </button>
      {revealed ? (
        <CopyButton value={value} label="Copy" className="btn btn-ghost btn-xs shrink-0 gap-1" />
      ) : null}
    </div>
  );
}
