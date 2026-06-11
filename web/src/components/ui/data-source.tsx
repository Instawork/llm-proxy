import { useEffect, useState } from "react";

export type DataSource = "memory" | "redis" | "redislive" | "client" | "config" | "dynamodb";

const META: Record<DataSource, { label: string; hint: string; className: string; dot: string }> = {
  memory: {
    label: "Memory",
    hint: "In-process today UTC — lost on proxy restart",
    className: "badge-warning badge-outline",
    dot: "bg-warning",
  },
  redis: {
    label: "Redis",
    hint: "Persisted daily rollups — survives restart (~30 days in charts)",
    className: "badge-info badge-outline",
    dot: "bg-info",
  },
  redislive: {
    label: "Redis + live",
    hint: "Live in-process value, also archived to Redis each day (survives restart)",
    className: "badge-info badge-outline",
    dot: "bg-info",
  },
  client: {
    label: "This tab",
    hint: "Browser-only sparkline while this page is open",
    className: "badge-ghost",
    dot: "bg-base-content/40",
  },
  config: {
    label: "Config",
    hint: "From YAML / environment",
    className: "badge-neutral badge-outline",
    dot: "bg-neutral",
  },
  dynamodb: {
    label: "DynamoDB",
    hint: "Persisted API key registry",
    className: "badge-success badge-outline",
    dot: "bg-success",
  },
};

export function dataSourceHint(source: DataSource, detail?: string): string {
  const base = META[source].hint;
  return detail ? `${base}. ${detail}` : base;
}

export function trendChartSource(dailyHistoryAvailable: boolean): DataSource {
  return dailyHistoryAvailable ? "redis" : "client";
}

export function rateLimitUsageSource(backend?: string): DataSource {
  return backend === "redis" ? "redis" : "memory";
}

export function circuitLiveSource(backend?: string): DataSource {
  return backend === "redis" ? "redis" : "memory";
}

export function DataSourceBadge({
  source,
  title,
}: {
  source: DataSource;
  title?: string;
}) {
  const meta = META[source];
  return (
    <span className={`badge badge-sm ${meta.className}`} title={title ?? meta.hint}>
      {meta.label}
    </span>
  );
}

export function DataSourceLegend() {
  const items: DataSource[] = ["memory", "redis", "client", "config", "dynamodb"];
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl bg-base-100/80 px-4 py-2.5 text-xs ring-1 ring-base-300/60">
      <span className="font-medium text-base-content/70">Where data lives</span>
      {items.map((source) => (
        <span key={source} className="inline-flex items-center gap-1.5 text-base-content/60">
          <DataSourceBadge source={source} />
          <span>{META[source].hint}</span>
        </span>
      ))}
    </div>
  );
}

const KEY_STORAGE = "admin:dataKeyOpen";
const KEY_ITEMS: { source: DataSource; durable: boolean }[] = [
  { source: "redis", durable: true },
  { source: "redislive", durable: true },
  { source: "dynamodb", durable: true },
  { source: "config", durable: true },
  { source: "memory", durable: false },
  { source: "client", durable: false },
];

/**
 * Floating, collapsible "data key" that explains every source badge. Slides
 * out from the right; open state persists in localStorage so it stays where
 * the operator left it.
 */
export function DataSourceKey() {
  const [open, setOpen] = useState(false);

  useEffect(() => {
    setOpen(window.localStorage.getItem(KEY_STORAGE) === "1");
  }, []);

  const toggle = () => {
    setOpen((prev) => {
      const next = !prev;
      window.localStorage.setItem(KEY_STORAGE, next ? "1" : "0");
      return next;
    });
  };

  return (
    <>
      <button
        type="button"
        onClick={toggle}
        className="btn btn-sm fixed bottom-5 right-5 z-50 gap-2 rounded-full shadow-lg"
        aria-expanded={open}
        aria-label="Toggle data source key"
      >
        <svg viewBox="0 0 24 24" className="h-4 w-4 fill-none stroke-current" strokeWidth="1.8">
          <circle cx="12" cy="12" r="9" />
          <path d="M12 16v-4M12 8h.01" />
        </svg>
        Data key
      </button>

      {open ? (
        <button
          type="button"
          aria-label="Close data source key"
          className="fixed inset-0 z-40 bg-black/20 backdrop-blur-[1px]"
          onClick={toggle}
        />
      ) : null}

      <aside
        className={`fixed bottom-0 right-0 top-0 z-50 flex w-80 max-w-[88vw] flex-col border-l border-base-300/70 bg-base-100 shadow-2xl transition-transform duration-200 ${open ? "translate-x-0" : "translate-x-full"
          }`}
      >
        <div className="flex items-center justify-between border-b border-base-300/70 px-5 py-4">
          <div>
            <h3 className="font-semibold">Where data lives</h3>
            <p className="text-xs text-base-content/60">What each badge means</p>
          </div>
          <button type="button" className="btn btn-ghost btn-sm btn-square" onClick={toggle} aria-label="Close">
            <svg viewBox="0 0 24 24" className="h-4 w-4 fill-none stroke-current" strokeWidth="2">
              <path d="M6 6l12 12M18 6 6 18" />
            </svg>
          </button>
        </div>

        <div className="flex-1 space-y-5 overflow-y-auto px-5 py-4">
          <KeyGroup title="Durable" caption="Survives proxy restarts">
            {KEY_ITEMS.filter((i) => i.durable).map((i) => (
              <KeyRow key={i.source} source={i.source} />
            ))}
          </KeyGroup>
          <KeyGroup title="Ephemeral" caption="Cleared on restart / tab close">
            {KEY_ITEMS.filter((i) => !i.durable).map((i) => (
              <KeyRow key={i.source} source={i.source} />
            ))}
          </KeyGroup>
        </div>
      </aside>
    </>
  );
}

function KeyGroup({
  title,
  caption,
  children,
}: {
  title: string;
  caption: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <div className="mb-2">
        <p className="text-[0.65rem] font-semibold uppercase tracking-[0.16em] text-base-content/40">{title}</p>
        <p className="text-xs text-base-content/50">{caption}</p>
      </div>
      <div className="space-y-2">{children}</div>
    </div>
  );
}

function KeyRow({ source }: { source: DataSource }) {
  const meta = META[source];
  return (
    <div className="flex items-start gap-3 rounded-xl bg-base-200/50 px-3 py-2.5">
      <span className={`mt-1 inline-block h-2.5 w-2.5 shrink-0 rounded-full ${meta.dot}`} />
      <div className="min-w-0">
        <DataSourceBadge source={source} />
        <p className="mt-1 text-xs text-base-content/60">{meta.hint}</p>
      </div>
    </div>
  );
}

/** Segmented Today · 7d · 30d selector for history-backed views. */
export function RangeToggle<T extends string>({
  value,
  options,
  onChange,
}: {
  value: T;
  options: { key: T; label: string }[];
  onChange: (next: T) => void;
}) {
  return (
    <div className="join">
      {options.map((opt) => (
        <button
          key={opt.key}
          type="button"
          onClick={() => onChange(opt.key)}
          className={`btn join-item btn-xs ${value === opt.key ? "btn-primary" : "btn-ghost"}`}
        >
          {opt.label}
        </button>
      ))}
    </div>
  );
}

export function LiveStat({
  title,
  value,
  hint,
  source,
  valueClassName = "text-2xl",
}: {
  title: string;
  value: React.ReactNode;
  hint?: string;
  source: DataSource;
  valueClassName?: string;
}) {
  return (
    <div className="stat glass-panel px-5 py-4">
      <div className="flex items-start justify-between gap-2">
        <div className="stat-title">{title}</div>
        <DataSourceBadge source={source} />
      </div>
      <div className={`stat-value ${valueClassName}`}>{value}</div>
      {hint ? <div className="stat-desc">{hint}</div> : null}
    </div>
  );
}

export function SectionPanel({
  title,
  subtitle,
  source,
  children,
}: {
  title: string;
  subtitle?: string;
  source?: DataSource;
  children: React.ReactNode;
}) {
  return (
    <div className="glass-panel overflow-hidden">
      <div className="border-b border-base-300/70 px-5 py-4">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="font-semibold">{title}</h3>
          {source ? <DataSourceBadge source={source} /> : null}
        </div>
        {subtitle ? <p className="mt-1 text-sm text-base-content/60">{subtitle}</p> : null}
      </div>
      {children}
    </div>
  );
}
