import { describe, expect, it } from "vitest";

import {
  capFraction,
  overviewScopeCopy,
  parseSpendView,
  rowInSpendTotal,
  sortBySpend,
  unmeteredEndpointSummary,
} from "./spend-overview";
import type { KeyCostMonthStats, KeyCostStats } from "../types";

function today(spend: number): KeyCostStats {
  return {
    source: "redis",
    spend_usd: spend,
    input_spend_usd: 0,
    output_spend_usd: 0,
    requests: 0,
    input_tokens: 0,
    output_tokens: 0,
  };
}

function month(spend: number): KeyCostMonthStats {
  return { month: "2026-09", spend_usd: spend, source: "redis" };
}

describe("capFraction", () => {
  it("measures monthly caps against month spend", () => {
    expect(
      capFraction({ cap: { period: "monthly", cents: 2000 }, today: today(1), month: month(15) }),
    ).toEqual({ spentUsd: 15, limitCents: 2000, fraction: 0.75, label: "Monthly limit" });
  });

  it("measures daily caps against today's spend", () => {
    expect(
      capFraction({ cap: { period: "daily", cents: 500 }, today: today(1), month: month(15) }),
    ).toEqual({ spentUsd: 1, limitCents: 500, fraction: 0.2, label: "Daily limit" });
  });

  it("returns null for uncapped keys", () => {
    expect(capFraction({ today: today(1), month: month(15) })).toBeNull();
    expect(capFraction({ cap: { period: "daily", cents: 0 }, today: today(1), month: month(15) })).toBeNull();
  });
});

describe("sortBySpend", () => {
  it("orders by month, then today, then id without mutating the input", () => {
    const rows = [
      { id: "b", today: today(1), month: month(5) },
      { id: "c", today: today(9), month: month(5) },
      { id: "a", today: today(1), month: month(5) },
      { id: "d", today: today(0), month: month(50) },
    ];
    const sorted = sortBySpend(rows, (r) => r.id);
    expect(sorted.map((r) => r.id)).toEqual(["d", "c", "a", "b"]);
    expect(rows.map((r) => r.id)).toEqual(["b", "c", "a", "d"]);
  });
});

describe("unmeteredEndpointSummary", () => {
  const endpoints = [
    { endpoint: "/v1/embeddings", requests: 5 },
    { endpoint: "/v1/models", requests: 2 },
    { endpoint: "/v1/chat/completions (HTTP 401)", requests: 1 },
  ];

  it("lists the busiest endpoints and counts the rest", () => {
    expect(unmeteredEndpointSummary({ endpoints })).toBe("/v1/embeddings, /v1/models +1 more");
  });

  it("omits the overflow note when everything fits", () => {
    expect(unmeteredEndpointSummary({ endpoints }, 3)).toBe(
      "/v1/embeddings, /v1/models, /v1/chat/completions (HTTP 401)",
    );
    expect(unmeteredEndpointSummary({ endpoints: [] })).toBe("");
  });
});

describe("spend view", () => {
  it("parses the scope param and defaults to my spend", () => {
    expect(parseSpendView("all")).toBe("all");
    expect(parseSpendView("shared")).toBe("shared");
    expect(parseSpendView("mine")).toBe("mine");
    expect(parseSpendView("bogus")).toBe("mine");
    expect(parseSpendView(null)).toBe("mine");
  });

  it("decides which rows count toward each view", () => {
    const own = {};
    const shared = { shared: true };
    expect(rowInSpendTotal(own, "mine")).toBe(true);
    expect(rowInSpendTotal(shared, "mine")).toBe(false);
    expect(rowInSpendTotal(own, "shared")).toBe(false);
    expect(rowInSpendTotal(shared, "shared")).toBe(true);
    expect(rowInSpendTotal(own, "all")).toBe(true);
    expect(rowInSpendTotal(shared, "all")).toBe(true);
  });
});

describe("overviewScopeCopy", () => {
  it("labels my spend as keys you own and counts excluded shared keys", () => {
    const copy = overviewScopeCopy("mine", { owned: 2, shared: 2 });
    expect(copy.totalsTitle).toBe("Your spend (keys you own)");
    expect(copy.scopePhrase).toBe("keys you own");
    expect(copy.excludesNote).toBe("Excludes 2 shared keys — see each key for its spend.");
    expect(copy.rowNote({ shared: true })).toBe("Not included in your spend total");
    expect(copy.rowNote({})).toBeNull();
  });

  it("uses the singular and omits the note when nothing is excluded", () => {
    expect(overviewScopeCopy("mine", { owned: 1, shared: 1 }).excludesNote).toBe(
      "Excludes 1 shared key — see each key for its spend.",
    );
    expect(overviewScopeCopy("mine", { owned: 1, shared: 0 }).excludesNote).toBeNull();
  });

  it("labels shared key spend and notes the owned keys it leaves out", () => {
    const copy = overviewScopeCopy("shared", { owned: 2, shared: 3 });
    expect(copy.totalsTitle).toBe("Shared key spend (keys shared with you)");
    expect(copy.excludesNote).toBe("Excludes your 2 own keys — see each key for its spend.");
    expect(copy.rowNote({})).toBe("Not included in the shared key spend total");
    expect(copy.rowNote({ shared: true })).toBeNull();
  });

  it("covers every visible key in the all view", () => {
    const copy = overviewScopeCopy("all", { owned: 2, shared: 3 });
    expect(copy.totalsTitle).toContain("All spend");
    expect(copy.excludesNote).toBeNull();
    expect(copy.rowNote({ shared: true })).toBeNull();
    expect(copy.rowNote({})).toBeNull();
  });

  it("leaves the fleet view unlabelled", () => {
    const copy = overviewScopeCopy(null, { owned: 0, shared: 0 });
    expect(copy.totalsTitle).toBe("Spend");
    expect(copy.scopePhrase).toBe("");
    expect(copy.excludesNote).toBeNull();
  });
});
