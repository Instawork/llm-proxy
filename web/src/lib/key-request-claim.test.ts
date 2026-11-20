import { describe, expect, it } from "vitest";

import { APIClientError } from "../client";
import type { KeyRequestRecord } from "../types";
import { canResetClaim, claimErrorMessage, claimKeyPath } from "./key-request-claim";

function request(overrides: Partial<KeyRequestRecord>): KeyRequestRecord {
  return {
    id: "req-1",
    requester_email: "viewer@example.com",
    provider: "openai",
    name: "svc",
    description: "svc",
    status: "approved",
    created_at: "2026-11-20T00:00:00Z",
    updated_at: "2026-11-20T00:00:00Z",
    claimable: false,
    ...overrides,
  };
}

describe("claimKeyPath", () => {
  it("builds the keys page claim link", () => {
    expect(claimKeyPath("a b")).toBe("/keys?claim=a%20b");
  });
});

describe("canResetClaim", () => {
  it("is true only for approved requests that were claimed", () => {
    expect(canResetClaim(request({ claimed_at: "2026-11-20T01:00:00Z" }))).toBe(true);
    expect(canResetClaim(request({ claimable: true }))).toBe(false);
    expect(canResetClaim(request({ status: "pending", claimed_at: "x" }))).toBe(false);
  });
});

describe("claimErrorMessage", () => {
  it("explains each claim failure", () => {
    expect(claimErrorMessage(new APIClientError(404, "x"))).toMatch(/not found/);
    expect(claimErrorMessage(new APIClientError(409, "x"))).toMatch(/not been approved/);
    expect(claimErrorMessage(new APIClientError(410, "x"))).toMatch(/already claimed/);
    expect(claimErrorMessage(new APIClientError(403, "x"))).toMatch(/blocked/);
    expect(claimErrorMessage(new Error("boom"))).toBe("boom");
    expect(claimErrorMessage("nope")).toBe("Failed to claim key");
  });
});
