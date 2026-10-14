import { describe, expect, it } from "vitest";

import { invalidEmails, parseEmailInput } from "./key-viewers-panel";

describe("parseEmailInput", () => {
  it("splits on commas, semicolons and whitespace, lowercases and dedupes", () => {
    expect(parseEmailInput(" A@x.com, b@x.com;\nc@x.com  a@X.com ")).toEqual([
      "a@x.com",
      "b@x.com",
      "c@x.com",
    ]);
  });

  it("returns an empty list for blank input", () => {
    expect(parseEmailInput("  , ; ")).toEqual([]);
  });
});

describe("invalidEmails", () => {
  it("flags entries that are not email addresses", () => {
    expect(invalidEmails(["ok@x.com", "nope", "a@b", "x@y.co"])).toEqual(["nope", "a@b"]);
  });
});
