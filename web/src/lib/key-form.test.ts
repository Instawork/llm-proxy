import { describe, expect, it } from "vitest";

import {
  keyFormFromRecord,
  keyUpdateFromForm,
  piiDisplayLabel,
  piiFieldPolicy,
} from "./key-form";
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

describe("piiDisplayLabel", () => {
  it("shows plain labels when overrides are allowed or still loading", () => {
    expect(piiDisplayLabel(null, true, "openai")).toBe("Inherit");
    expect(piiDisplayLabel(true, true, "openai")).toBe("On");
    expect(piiDisplayLabel(false, true, "openai")).toBe("Off");
    expect(piiDisplayLabel(false, undefined, "openai")).toBe("Off");
    expect(piiDisplayLabel(true, undefined, "openai")).toBe("On");
  });

  it("annotates ignored overrides on non-Bedrock keys when disabled", () => {
    expect(piiDisplayLabel(null, false, "openai")).toBe("Inherit");
    expect(piiDisplayLabel(false, false, "openai")).toBe("Off (ignored)");
    expect(piiDisplayLabel(true, false, "openai")).toBe("On (ignored)");
  });

  it("distinguishes SigV4-exempt Bedrock Off from ignored Bedrock On", () => {
    expect(piiDisplayLabel(false, false, "bedrock")).toBe("Off (SigV4 exempt)");
    expect(piiDisplayLabel(true, false, "bedrock")).toBe("On (ignored)");
    expect(piiDisplayLabel(null, false, "bedrock")).toBe("Inherit");
  });
});

describe("piiFieldPolicy", () => {
  it("leaves controls enabled while allowPerKeyOverride is undefined (loading/error) or true", () => {
    expect(piiFieldPolicy(undefined, "openai", "inherit")).toEqual({
      selectDisabled: false,
      onDisabled: false,
      offDisabled: false,
      overridesDisabled: false,
      isBedrockExempt: false,
    });
    expect(piiFieldPolicy(true, "openai", "off")).toEqual({
      selectDisabled: false,
      onDisabled: false,
      offDisabled: false,
      overridesDisabled: false,
      isBedrockExempt: false,
    });
  });

  it("locks non-Bedrock inherit keys when disabled, but allows clearing legacy overrides", () => {
    expect(piiFieldPolicy(false, "openai", "inherit", null)).toEqual({
      selectDisabled: true,
      onDisabled: true,
      offDisabled: true,
      overridesDisabled: true,
      isBedrockExempt: false,
    });
    expect(piiFieldPolicy(false, "openai", "off", false)).toEqual({
      selectDisabled: false,
      onDisabled: true,
      offDisabled: true,
      overridesDisabled: true,
      isBedrockExempt: false,
    });
    // Keeps select enabled even after switching a legacy key to inherit before saving.
    expect(piiFieldPolicy(false, "openai", "inherit", false)).toEqual({
      selectDisabled: false,
      onDisabled: true,
      offDisabled: true,
      overridesDisabled: true,
      isBedrockExempt: false,
    });
  });

  it("allows Off and Inherit on SigV4 Bedrock keys even when overrides are disabled", () => {
    expect(piiFieldPolicy(false, "bedrock", "inherit", null)).toEqual({
      selectDisabled: false,
      onDisabled: true,
      offDisabled: false,
      overridesDisabled: true,
      isBedrockExempt: true,
    });
  });
});
