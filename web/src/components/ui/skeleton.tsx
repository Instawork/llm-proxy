// Skeleton loaders give the dashboard a modern "content is coming" feel
// (as in Helicone/Portkey) instead of a centered spinner. daisyUI's
// `skeleton` class provides the shimmer animation.

export function StatSkeleton({ count = 4 }: { count?: number }) {
  return (
    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      {Array.from({ length: count }).map((_, i) => (
        <div key={i} className="glass-panel space-y-3 px-5 py-4">
          <div className="skeleton h-3 w-20" />
          <div className="skeleton h-7 w-24" />
          <div className="skeleton h-3 w-28" />
        </div>
      ))}
    </div>
  );
}

export function ChartSkeleton({ height = 220 }: { height?: number }) {
  return (
    <div className="glass-panel space-y-4 p-5">
      <div className="skeleton h-4 w-40" />
      <div className="skeleton w-full" style={{ height }} />
    </div>
  );
}

// PageSkeleton is the default loading state for data pages: a stat row plus
// a couple of chart blocks.
export function PageSkeleton() {
  return (
    <div className="space-y-6">
      <StatSkeleton />
      <div className="grid gap-4 lg:grid-cols-3">
        <div className="lg:col-span-2">
          <ChartSkeleton />
        </div>
        <ChartSkeleton />
      </div>
    </div>
  );
}
