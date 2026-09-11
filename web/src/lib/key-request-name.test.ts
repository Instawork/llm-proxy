import { describe, expect, it } from "vitest";

import { KEY_REQUEST_NAME_MAX, slugifyKeyName } from "./key-request-name";

describe("slugifyKeyName", () => {
  it("lowercases and hyphenates spaces", () => {
    expect(slugifyKeyName("Finch Worker")).toBe("finch-worker");
  });

  it("collapses punctuation runs into a single hyphen", () => {
    expect(slugifyKeyName("my_service (prod)!!")).toBe("my-service-prod");
  });

  it("trims leading and trailing hyphens", () => {
    expect(slugifyKeyName("--edge--")).toBe("edge");
  });

  it("returns empty string for all-punctuation input", () => {
    expect(slugifyKeyName("!!!")).toBe("");
  });

  it("truncates to the max length without a trailing hyphen", () => {
    const input = "a".repeat(39) + " b";
    const result = slugifyKeyName(input);
    expect(result).toBe("a".repeat(39));
    expect(result.length).toBeLessThanOrEqual(KEY_REQUEST_NAME_MAX);
  });
});
