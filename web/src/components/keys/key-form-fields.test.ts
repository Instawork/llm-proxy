import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { defaultKeyForm, type KeyFormState } from "../../lib/key-form";
import type { APIKey } from "../../types";
import { PiiFields } from "./key-form-fields";

const noopSetForm = (() => {}) as React.Dispatch<React.SetStateAction<KeyFormState>>;

function makeKey(overrides: Partial<APIKey> = {}): APIKey {
  return {
    key: "sk-iw-test",
    provider: "openai",
    description: "test key",
    daily_cost_limit: 10000,
    monthly_cost_limit: 0,
    enabled: true,
    redact_pii: null,
    rate_limit_rpm: 0,
    rate_limit_tpm: 0,
    rate_limit_rpd: 0,
    rate_limit_tpd: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    expires_at: null,
    ...overrides,
  };
}

describe("PiiFields", () => {
  it("does not show disabled banner while allowPerKeyOverride is undefined (loading/error)", () => {
    const html = renderToStaticMarkup(
      createElement(PiiFields, {
        form: { ...defaultKeyForm, provider: "openai", redact_pii: "inherit" },
        setForm: noopSetForm,
        editingKey: null,
        piiOffRequiresBedrock: false,
        allowPerKeyOverride: undefined,
      }),
    );
    expect(html).not.toContain("allow_per_key_override: false");
    expect(html).not.toMatch(/<select[^>]*disabled=""/);
  });

  it("disables select and options for new non-Bedrock key when overrides are disabled", () => {
    const html = renderToStaticMarkup(
      createElement(PiiFields, {
        form: { ...defaultKeyForm, provider: "openai", redact_pii: "inherit" },
        setForm: noopSetForm,
        editingKey: null,
        piiOffRequiresBedrock: false,
        allowPerKeyOverride: false,
      }),
    );
    expect(html).toContain("allow_per_key_override: false");
    expect(html).toContain("All requests follow the global setting.");
    expect(html).not.toContain("You can reset this key");
    expect(html).toMatch(/<select[^>]*disabled=""/);
    expect(html).toMatch(/<option value="on"[^>]*disabled=""/);
    expect(html).toMatch(/<option value="off"[^>]*disabled=""/);
  });

  it("keeps select enabled with reset guidance when editing a legacy non-Bedrock key", () => {
    const html = renderToStaticMarkup(
      createElement(PiiFields, {
        form: { ...defaultKeyForm, provider: "openai", redact_pii: "off" },
        setForm: noopSetForm,
        editingKey: makeKey({ provider: "openai", redact_pii: false }),
        piiOffRequiresBedrock: true,
        allowPerKeyOverride: false,
      }),
    );
    expect(html).toContain("allow_per_key_override: false");
    expect(html).toContain("You can reset this key to inherit the global default.");
    expect(html).not.toMatch(/<select[^>]*disabled=""/);
    expect(html).toMatch(/<option value="on"[^>]*disabled=""/);
    expect(html).toMatch(/<option value="off"[^>]*disabled=""/);
    // Does not double-stack the Bedrock warning when overrides are disabled.
    expect(html).not.toContain("Turning PII off requires a Bedrock key");
  });

  it("allows Off and Inherit on SigV4 Bedrock keys when overrides are disabled", () => {
    const html = renderToStaticMarkup(
      createElement(PiiFields, {
        form: { ...defaultKeyForm, provider: "bedrock", redact_pii: "inherit" },
        setForm: noopSetForm,
        editingKey: null,
        piiOffRequiresBedrock: false,
        allowPerKeyOverride: false,
      }),
    );
    expect(html).toContain("SigV4 Bedrock requests bypass wire-mode PII redaction");
    expect(html).not.toMatch(/<select[^>]*disabled=""/);
    expect(html).toMatch(/<option value="on"[^>]*disabled=""/);
    expect(html).not.toMatch(/<option value="off"[^>]*disabled=""/);
  });

  it("shows Bedrock warning when overrides are enabled and non-Bedrock key sets Off", () => {
    const html = renderToStaticMarkup(
      createElement(PiiFields, {
        form: { ...defaultKeyForm, provider: "openai", redact_pii: "off" },
        setForm: noopSetForm,
        editingKey: makeKey({ provider: "openai", redact_pii: null }),
        piiOffRequiresBedrock: true,
        allowPerKeyOverride: true,
      }),
    );
    expect(html).toContain("Turning PII off requires a Bedrock key");
    expect(html).not.toContain("allow_per_key_override: false");
  });

  it("shows unredacted PII warning for bypass-allowed user setting Off on non-Bedrock", () => {
    const html = renderToStaticMarkup(
      createElement(PiiFields, {
        form: { ...defaultKeyForm, provider: "openai", redact_pii: "off" },
        setForm: noopSetForm,
        editingKey: null,
        piiOffRequiresBedrock: false,
        allowPerKeyOverride: true,
      }),
    );
    expect(html).toContain("reach the provider with PII unredacted");
  });
});
