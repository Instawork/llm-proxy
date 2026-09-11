import { describe, expect, it } from "vitest";

import { keyFormFromRecord, keyUpdateFromForm } from "./key-form";
import type { APIKey } from "../types";

function baseRecord(overrides: Partial<APIKey> = {}): APIKey {
  return {
    key: "sk-iw-abc123",
    provider: "openai",
    description: "My key",
    daily_cost_limit: 10000,
    monthly_cost_limit: 0,
    enabled: true,
    redact_pii: null,
    rate_limit_rpm: 60,
    rate_limit_tpm: 0,
    rate_limit_rpd: 0,
    rate_limit_tpd: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    expires_at: null,
    ...overrides,
  };
}

describe("keyUpdateFromForm", () => {
  it("returns an empty patch when nothing changed", () => {
    const record = baseRecord();
    const form = keyFormFromRecord(record, "metered");
    expect(keyUpdateFromForm(record, form)).toEqual({});
  });

  it("omits expires_at when the stored expiry is left untouched", () => {
    const record = baseRecord({ expires_at: "2026-12-25T00:00:00.000Z" });
    const form = keyFormFromRecord(record, "metered");
    // Changing an unrelated field shouldn't re-encode the untouched expiry.
    form.rate_limit_rpm = "120";
    const update = keyUpdateFromForm(record, form);
    expect(update.expires_at).toBeUndefined();
    expect(update.rate_limit_rpm).toBe(120);
  });

  it("sends only the description when just the name changes", () => {
    const record = baseRecord();
    const form = keyFormFromRecord(record, "metered");
    form.description = "Renamed key";
    expect(keyUpdateFromForm(record, form)).toEqual({ description: "Renamed key" });
  });

  it("sends both cost fields together when the period switches", () => {
    const record = baseRecord({ daily_cost_limit: 10000, monthly_cost_limit: 0 });
    const form = keyFormFromRecord(record, "metered");
    form.cost_limit_period = "monthly";
    form.cost_limit_dollars = "50";
    expect(keyUpdateFromForm(record, form)).toEqual({
      daily_cost_limit: 0,
      monthly_cost_limit: 5000,
    });
  });

  it("sends expires_at: null when an existing expiry is cleared", () => {
    const record = baseRecord({ expires_at: "2026-12-25T00:00:00.000Z" });
    const form = keyFormFromRecord(record, "metered");
    form.expiry_preset = "never";
    form.expiry_custom = "";
    expect(keyUpdateFromForm(record, form)).toEqual({ expires_at: null });
  });
});
