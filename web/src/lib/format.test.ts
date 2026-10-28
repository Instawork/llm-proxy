import { describe, expect, it } from "vitest";

import { cachedInputHint, formatKeyMonthSpend, formatKeySpendCap } from "./format";
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

describe("formatKeySpendCap", () => {
  const base = { tags: undefined, owner_email: undefined, daily_cost_limit: 0, monthly_cost_limit: 0 };

  it("prefers the server-resolved cap on shared keys", () => {
    const spendWithCap = {
      ...spend(0),
      cap: { period: "monthly", cents: 5000 },
    } as unknown as SharedKeySpend;
    expect(formatKeySpendCap({ ...base, spend: spendWithCap })).toBe("$50.00/month");
  });

  it("falls back to the key's own limits", () => {
    expect(formatKeySpendCap({ ...base, daily_cost_limit: 500 })).toBe("$5.00/day");
  });
});

describe("cachedInputHint", () => {
  it("shows read and written cache tokens with their share of input", () => {
    expect(cachedInputHint(800, 150, 1000)).toBe("800 read · 150 written · 95.0% of input");
  });

  it("omits the share when the input total is unknown or zero", () => {
    expect(cachedInputHint(1200, 0)).toBe("1.2K read · 0 written");
    expect(cachedInputHint(0, 0, 0)).toBe("0 read · 0 written");
  });
});
