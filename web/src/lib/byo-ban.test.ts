import { describe, expect, it } from "vitest";

import { inferProviderFromMaskedId } from "./byo-ban";

describe("inferProviderFromMaskedId", () => {
  it("attributes OpenRouter credentials to the openrouter route", () => {
    expect(inferProviderFromMaskedId("sk-or-…abcd1234")).toBe("openrouter");
  });

  it("keeps OpenAI and Anthropic prefixes distinct from OpenRouter", () => {
    expect(inferProviderFromMaskedId("sk-proj-…abcd1234")).toBe("openai");
    expect(inferProviderFromMaskedId("sk-…abcd1234")).toBe("openai");
    expect(inferProviderFromMaskedId("sk-ant-…abcd1234")).toBe("anthropic");
  });
});
