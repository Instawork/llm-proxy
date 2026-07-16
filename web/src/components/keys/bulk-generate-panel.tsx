import type { Provider } from "../../types";
import { ProviderIcon } from "../ui/provider-badge";

export type BulkProviderStatus = "ready" | "missing" | "unavailable";

export type BulkProviderRow = {
  provider: Provider;
  status: BulkProviderStatus;
};

export function ProviderCoveragePills({ providers }: { providers: BulkProviderRow[] }) {
  return (
    <div className="flex flex-wrap gap-2" aria-label="Provider coverage">
      {providers.map((row) => (
        <StatusPill key={row.provider} provider={row.provider} status={row.status} />
      ))}
    </div>
  );
}

function StatusPill({ provider, status }: BulkProviderRow) {
  if (status === "ready") {
    return (
      <span className="inline-flex items-center gap-1.5 rounded-full border border-success/40 bg-success/10 px-2.5 py-1 text-xs font-medium text-success">
        <svg viewBox="0 0 20 20" className="h-3.5 w-3.5 fill-current" aria-hidden>
          <path
            fillRule="evenodd"
            d="M16.704 4.153a.75.75 0 0 1 .143 1.052l-8 10.5a.75.75 0 0 1-1.127.075l-4.5-4.5a.75.75 0 0 1 1.06-1.06l3.894 3.893 7.48-9.817a.75.75 0 0 1 1.05-.143Z"
            clipRule="evenodd"
          />
        </svg>
        <ProviderIcon provider={provider} size={12} />
        {provider}
      </span>
    );
  }
  if (status === "missing") {
    return (
      <span className="inline-flex items-center gap-1.5 rounded-full border border-warning/50 bg-warning/15 px-2.5 py-1 text-xs font-medium text-warning-content">
        <span className="h-1.5 w-1.5 rounded-full bg-warning" aria-hidden />
        <ProviderIcon provider={provider} size={12} />
        {provider}
        <span className="font-normal opacity-70">missing</span>
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full border border-base-300 bg-base-200/60 px-2.5 py-1 text-xs font-medium text-base-content/40">
      <ProviderIcon provider={provider} size={12} className="opacity-50" />
      {provider}
      <span className="font-normal">unavailable</span>
    </span>
  );
}

type BulkGeneratePanelProps = {
  providers: BulkProviderRow[];
  missingCount: number;
  busy: boolean;
  personal: boolean;
  onGenerate: () => void;
};

export default function BulkGeneratePanel({
  providers,
  missingCount,
  busy,
  personal,
  onGenerate,
}: BulkGeneratePanelProps) {
  const eligible = providers.filter((p) => p.status !== "unavailable").length;
  const label =
    missingCount === eligible
      ? personal
        ? "Generate Personal Keys"
        : "Generate Keys"
      : "Generate Missing Keys";

  return (
    <div className="glass-panel overflow-hidden">
      <div className="flex flex-col gap-5 px-5 py-5 sm:flex-row sm:items-center sm:justify-between sm:gap-6 sm:px-6">
        <div className="min-w-0 flex-1 space-y-3">
          <div>
            <h2 className="text-sm font-semibold text-base-content">
              {personal ? "Personal key coverage" : "Provider key coverage"}
            </h2>
            <p className="mt-0.5 text-sm text-base-content/60">
              {personal
                ? "One llm-proxy key per upstream route. Generate the missing ones below."
                : "Create an auto-provisioned org key for each missing provider."}
            </p>
          </div>
          <ProviderCoveragePills providers={providers} />
        </div>
        <button
          type="button"
          className="btn btn-primary shrink-0 px-6"
          disabled={busy || missingCount === 0}
          onClick={onGenerate}
        >
          {busy ? <span className="loading loading-spinner loading-sm" /> : null}
          {label}
        </button>
      </div>
    </div>
  );
}
