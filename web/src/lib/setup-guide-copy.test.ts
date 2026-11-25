import { describe, expect, it } from "vitest";

import { promptHint, snippetWarning } from "./setup-guide-copy";

describe("setup guide copy", () => {
  it("warns about a real key only for keys the user holds", () => {
    expect(snippetWarning(false)).toContain("contains your real proxy key");
  });

  it("describes the placeholder on shared keys instead of claiming a real key", () => {
    const warning = snippetWarning(true);
    expect(warning).toContain("YOUR_PROXY_KEY");
    expect(warning).not.toContain("contains your real proxy key");
  });

  it("does not tell shared-key viewers to copy a key from above", () => {
    expect(promptHint(false)).toContain("Copy the key from above");
    expect(promptHint(true)).not.toContain("from above");
  });
});
