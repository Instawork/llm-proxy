import { useMemo, useState } from "react";

import { DEFAULT_TOP_N } from "../lib/group-rows";

export function CollapsedTableFooter({
  showAll,
  onToggle,
  total,
  entityLabel,
  topN = DEFAULT_TOP_N,
}: {
  showAll: boolean;
  onToggle: () => void;
  total: number;
  entityLabel: string;
  topN?: number;
}) {
  return (
    <button type="button" className="btn btn-ghost btn-xs" onClick={onToggle}>
      {showAll ? `Show top ${topN}` : `Show all ${total} ${entityLabel}`}
    </button>
  );
}

export function useCollapsedRows<TRow, TDisplay extends { isOthers?: boolean }>(
  rows: TRow[],
  toDisplay: (rows: TRow[], topN: number, collapse: boolean) => TDisplay[],
  entityLabel: string,
  topN = DEFAULT_TOP_N,
) {
  const [showAll, setShowAll] = useState(false);
  const [searchActive, setSearchActive] = useState(false);

  const fullRows = useMemo(() => toDisplay(rows, topN, false), [rows, toDisplay, topN]);
  const collapsedRows = useMemo(() => toDisplay(rows, topN, true), [rows, toDisplay, topN]);
  const displayData = searchActive || showAll ? fullRows : collapsedRows;
  const hiddenCount = Math.max(0, rows.length - topN);
  const footer =
    hiddenCount > 0 && !searchActive ? (
      <CollapsedTableFooter
        showAll={showAll}
        onToggle={() => setShowAll((value) => !value)}
        total={rows.length}
        entityLabel={entityLabel}
        topN={topN}
      />
    ) : null;

  return { displayData, onSearchActiveChange: setSearchActive, footer, collapseEnabled: hiddenCount > 0 };
}
