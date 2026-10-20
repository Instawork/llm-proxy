import { describe, expect, it } from "vitest";

import { formatKeyMonthSpend } from "./format";
import type { SharedKeySpend } from "../types";

function spend(monthUsd: number): SharedKeySpend {
  return {
    today: { spend_usd: 0, requests: 0 },
    month: { month: "2026-10", spend_usd: monthUsd },
    rollup_available: true,
  } as unknown as SharedKeySpend;
}

describe("formatKeyMonthSpend", () => {
  it("shows a dash when the list row carries no spend", () => {
    expect(formatKeyMonthSpend({})).toBe("—");
  });

  it("formats month-to-date spend", () => {
    expect(formatKeyMonthSpend({ spend: spend(0) })).toBe("$0.00");
    expect(formatKeyMonthSpend({ spend: spend(12.345) })).toBe("$12.35");
  });
});
