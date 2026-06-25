import { DataSourceBadge, type DataSource } from "./data-source";
import { SpendLimitProgress } from "./spend-limit-progress";
import { formatUsd } from "../../lib/format";

/** Hero spend summary: today + month with limit progress bars. */
export function SpendOverview({
  todayUsd,
  monthUsd,
  monthLabel,
  dailyLimitCents,
  monthlyLimitCents,
  costSource,
  monthSource,
  showDailyLimit,
  showMonthlyLimit = true,
}: {
  todayUsd: number;
  monthUsd: number;
  monthLabel: string;
  dailyLimitCents: number;
  monthlyLimitCents: number;
  costSource: DataSource;
  monthSource: DataSource;
  /** Show daily limit progress when the key uses a daily cap. */
  showDailyLimit: boolean;
  /** Show monthly limit progress when the key uses a monthly cap. */
  showMonthlyLimit?: boolean;
}) {
  return (
    <div className="glass-panel p-5">
      <div className="mb-4">
        <h3 className="font-semibold">Spend</h3>
        <p className="text-sm text-base-content/60">UTC day and calendar month · fleet rollups where available</p>
      </div>
      <div className="grid gap-6 lg:grid-cols-2">
        <SpendOverviewColumn
          label="Today"
          hint="UTC calendar day"
          spentUsd={todayUsd}
          limitCents={showDailyLimit ? dailyLimitCents : 0}
          limitLabel={showDailyLimit ? "Daily limit" : undefined}
          source={costSource}
        />
        <SpendOverviewColumn
          label="This month"
          hint={monthLabel}
          spentUsd={monthUsd}
          limitCents={showMonthlyLimit ? monthlyLimitCents : 0}
          limitLabel={showMonthlyLimit ? "Monthly limit" : undefined}
          source={monthSource}
        />
      </div>
    </div>
  );
}

function SpendOverviewColumn({
  label,
  hint,
  spentUsd,
  limitCents,
  limitLabel,
  source,
}: {
  label: string;
  hint: string;
  spentUsd: number;
  limitCents: number;
  limitLabel?: string;
  source: DataSource;
}) {
  return (
    <div className="space-y-2">
      <div className="flex items-start justify-between gap-2">
        <div>
          <p className="text-sm font-medium text-base-content/80">{label}</p>
          <p className="text-xs text-base-content/50">{hint}</p>
        </div>
        <DataSourceBadge source={source} />
      </div>
      <p className="text-3xl font-semibold tracking-tight">{formatUsd(spentUsd)}</p>
      {limitLabel ? (
        <SpendLimitProgress spentUsd={spentUsd} limitCents={limitCents} label={limitLabel} />
      ) : null}
    </div>
  );
}

export function SpendPeriodPanel({
  title,
  subtitle,
  source,
  spentUsd,
  limitCents,
  limitLabel,
  children,
}: {
  title: string;
  subtitle?: string;
  source: DataSource;
  spentUsd: number;
  limitCents?: number;
  limitLabel?: string;
  children?: React.ReactNode;
}) {
  return (
    <div className="rounded-xl border border-base-300/70 bg-base-100/40 p-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div>
          <h4 className="font-medium">{title}</h4>
          {subtitle ? <p className="text-sm text-base-content/60">{subtitle}</p> : null}
        </div>
        <DataSourceBadge source={source} />
      </div>
      <div className="mb-3">
        <p className="text-xs uppercase tracking-wide text-base-content/50">Total spend</p>
        <p className="text-xl font-semibold">{formatUsd(spentUsd)}</p>
      </div>
      {limitLabel ? (
        <div className="mb-3">
          <SpendLimitProgress
            spentUsd={spentUsd}
            limitCents={limitCents ?? 0}
            label={limitLabel}
          />
        </div>
      ) : null}
      {children}
    </div>
  );
}
