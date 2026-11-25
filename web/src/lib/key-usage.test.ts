import { describe, expect, it } from "vitest";

import { keyHasBeenUsed } from "./key-usage";

describe("keyHasBeenUsed", () => {
  it("is false with no marker and no stats", () => {
    expect(keyHasBeenUsed({}, undefined)).toBe(false);
    expect(keyHasBeenUsed(undefined, {})).toBe(false);
  });

  it("trusts the persisted first-request marker", () => {
    expect(keyHasBeenUsed({ first_request_at: "2026-10-01T00:00:00Z" }, {})).toBe(true);
  });

  it("falls back to live stats", () => {
    expect(keyHasBeenUsed({}, { cost_today: { requests: 2 } } as never)).toBe(true);
    expect(keyHasBeenUsed({}, { cost_month: { spend_usd: 0.01 } } as never)).toBe(true);
    expect(keyHasBeenUsed({}, { unmetered_today: { requests: 1 } } as never)).toBe(true);
    expect(keyHasBeenUsed({}, { recent_cost: [{}] } as never)).toBe(true);
  });

  it("stays false when every counter is zero", () => {
    expect(
      keyHasBeenUsed({}, { cost_today: { requests: 0 }, cost_month: { spend_usd: 0 }, recent_cost: [] } as never),
    ).toBe(false);
  });
});
