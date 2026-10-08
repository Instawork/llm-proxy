import { describe, expect, it } from "vitest";

import { n8nSetupGuide } from "./n8n-setup";

describe("n8nSetupGuide", () => {
  it("routes OpenRouter through an OpenAI credential at the openrouter base URL", () => {
    const guide = n8nSetupGuide("openrouter", "https://llm.example.com/openrouter/api/v1");
    expect(guide?.credentialLabel).toBe("OpenAI");
    expect(guide?.urlField).toBe("Base URL");
    expect(guide?.steps.join("\n")).toContain("https://llm.example.com/openrouter/api/v1");
  });
});
