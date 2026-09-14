import { describe, expect, it } from "vitest";

import { keyDisplayRows, uniformOr, type KeyStackRow } from "./key-stacks";
import type { APIKey } from "../types";

let counter = 0;

function makeKey(overrides: Partial<APIKey> = {}): APIKey {
  counter += 1;
  return {
    key: `sk-iw-${counter}`,
    provider: "openai",
    description: "",
    daily_cost_limit: 0,
    enabled: true,
    created_at: `2026-01-0${counter}T00:00:00Z`,
    updated_at: `2026-01-0${counter}T00:00:00Z`,
    ...overrides,
  };
}

function personal(overrides: Partial<APIKey> = {}): APIKey {
  return makeKey({ owner_email: "eric@instawork.com", description: "eric", ...overrides });
}

describe("keyDisplayRows", () => {
  it("stacks two personal keys with the same owner and name across providers", () => {
    const a = personal({ provider: "openai" });
    const b = personal({ provider: "anthropic" });
    const rows = keyDisplayRows([a, b]);
    expect(rows).toHaveLength(1);
    const stack = rows[0] as KeyStackRow;
    expect(stack.kind).toBe("stack");
    expect(stack.keys).toEqual([a, b]);
    expect(stack.subRows.map((r) => r.key)).toEqual([a, b]);
    expect(stack.id).toBe("stack:eric@instawork.com:eric");
  });

  it("returns a single personal key as a leaf, not a stack", () => {
    const a = personal();
    const rows = keyDisplayRows([a]);
    expect(rows).toEqual([{ kind: "key", id: a.key, key: a }]);
  });

  it("keeps non-personal keys with identical descriptions as separate leaves", () => {
    const a = makeKey({ description: "shared" });
    const b = makeKey({ description: "shared" });
    const rows = keyDisplayRows([a, b]);
    expect(rows).toEqual([
      { kind: "key", id: a.key, key: a },
      { kind: "key", id: b.key, key: b },
    ]);
  });

  it("does not merge the same name across different owners", () => {
    const a = personal({ owner_email: "eric@instawork.com" });
    const b = personal({ owner_email: "other@instawork.com" });
    const rows = keyDisplayRows([a, b]);
    expect(rows).toHaveLength(2);
    expect(rows.every((r) => r.kind === "key")).toBe(true);
  });

  it("does not merge different names for the same owner", () => {
    const a = personal({ description: "eric" });
    const b = personal({ description: "eric-2" });
    const rows = keyDisplayRows([a, b]);
    expect(rows).toHaveLength(2);
    expect(rows.every((r) => r.kind === "key")).toBe(true);
  });

  it("normalizes owner email case and description whitespace", () => {
    const a = personal({ owner_email: "Eric@Instawork.com", description: " eric " });
    const b = personal({ owner_email: "eric@instawork.com", description: "eric" });
    const rows = keyDisplayRows([a, b]);
    expect(rows).toHaveLength(1);
    const stack = rows[0] as KeyStackRow;
    expect(stack.ownerEmail).toBe("eric@instawork.com");
    expect(stack.name).toBe("eric");
  });

  it("treats tags.personal without owner_email as a leaf", () => {
    const a = makeKey({ tags: { personal: "true" }, description: "eric" });
    const b = makeKey({ tags: { personal: "true" }, description: "eric" });
    const rows = keyDisplayRows([a, b]);
    expect(rows).toEqual([
      { kind: "key", id: a.key, key: a },
      { kind: "key", id: b.key, key: b },
    ]);
  });

  it("treats a blank description as a leaf even for the same owner", () => {
    const a = personal({ description: "" });
    const b = personal({ description: "   " });
    const rows = keyDisplayRows([a, b]);
    expect(rows).toHaveLength(2);
    expect(rows.every((r) => r.kind === "key")).toBe(true);
  });

  it("orders stack keys and providers by KEY_PROVIDERS regardless of input order", () => {
    const bedrock = personal({ provider: "bedrock" });
    const openai = personal({ provider: "openai" });
    const gemini = personal({ provider: "gemini" });
    const rows = keyDisplayRows([bedrock, openai, gemini]);
    const stack = rows[0] as KeyStackRow;
    expect(stack.keys.map((k) => k.provider)).toEqual(["openai", "gemini", "bedrock"]);
    expect(stack.providers).toEqual(["openai", "gemini", "bedrock"]);
  });

  it("places the stack at its first key's position and preserves other rows' relative order", () => {
    const solo = makeKey({ description: "solo" });
    const a = personal({ provider: "openai" });
    const other = makeKey({ description: "other" });
    const b = personal({ provider: "anthropic" });
    const rows = keyDisplayRows([solo, a, other, b]);
    expect(rows.map((r) => (r.kind === "stack" ? "stack" : r.key.description))).toEqual([
      "solo",
      "stack",
      "other",
    ]);
  });

  it("computes enabledCount and the earliest created_at", () => {
    const a = personal({ enabled: true, created_at: "2026-02-01T00:00:00Z", provider: "openai" });
    const b = personal({ enabled: false, created_at: "2026-01-01T00:00:00Z", provider: "anthropic" });
    const rows = keyDisplayRows([a, b]);
    const stack = rows[0] as KeyStackRow;
    expect(stack.enabledCount).toBe(1);
    expect(stack.created_at).toBe("2026-01-01T00:00:00Z");
  });

  it("returns every key as a leaf when stack is false", () => {
    const a = personal({ provider: "openai" });
    const b = personal({ provider: "anthropic" });
    const rows = keyDisplayRows([a, b], { stack: false });
    expect(rows).toEqual([
      { kind: "key", id: a.key, key: a },
      { kind: "key", id: b.key, key: b },
    ]);
  });

  it("returns an empty array for empty input, and every row id is unique", () => {
    expect(keyDisplayRows([])).toEqual([]);
    const a = personal({ provider: "openai" });
    const b = personal({ provider: "anthropic" });
    const solo = makeKey({ description: "solo" });
    const rows = keyDisplayRows([a, b, solo]);
    const ids = rows.map((r) => r.id);
    expect(new Set(ids).size).toBe(ids.length);
  });
});

describe("uniformOr", () => {
  it("returns the shared value when every key agrees", () => {
    const a = makeKey({ enabled: true });
    const b = makeKey({ enabled: true });
    expect(uniformOr([a, b], (k) => k.enabled, "Mixed")).toBe(true);
  });

  it("returns the fallback when keys disagree", () => {
    const a = makeKey({ enabled: true });
    const b = makeKey({ enabled: false });
    expect(uniformOr([a, b], (k) => k.enabled, "Mixed")).toBe("Mixed");
  });

  it("returns the single key's value for a one-element input", () => {
    const a = makeKey({ enabled: false });
    expect(uniformOr([a], (k) => k.enabled, "Mixed")).toBe(false);
  });
});
