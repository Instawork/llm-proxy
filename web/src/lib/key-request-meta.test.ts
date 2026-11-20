import { describe, expect, it } from "vitest";

import {
  formatRequestLimit,
  keyRequestSourceLabel,
  keyRequestStatusBadgeClass,
  safeTicketUrl,
} from "./key-request-meta";

describe("keyRequestSourceLabel", () => {
  it("labels requests without a source as dashboard", () => {
    expect(keyRequestSourceLabel(undefined)).toBe("dashboard");
    expect(keyRequestSourceLabel("")).toBe("dashboard");
  });

  it("shows the integration caller id", () => {
    expect(keyRequestSourceLabel("init")).toBe("init");
  });
});

describe("safeTicketUrl", () => {
  it("keeps https URLs", () => {
    expect(safeTicketUrl("https://tickets.example.com/IH-1")).toBe(
      "https://tickets.example.com/IH-1",
    );
  });

  it("drops non-https and malformed URLs", () => {
    expect(safeTicketUrl("http://tickets.example.com/IH-1")).toBeUndefined();
    expect(safeTicketUrl("javascript:alert(1)")).toBeUndefined();
    expect(safeTicketUrl("not a url")).toBeUndefined();
    expect(safeTicketUrl(undefined)).toBeUndefined();
  });
});

describe("keyRequestStatusBadgeClass", () => {
  it("maps each status to a badge", () => {
    expect(keyRequestStatusBadgeClass("pending")).toBe("badge badge-warning");
    expect(keyRequestStatusBadgeClass("approving")).toBe("badge badge-warning");
    expect(keyRequestStatusBadgeClass("approved")).toBe("badge badge-success");
    expect(keyRequestStatusBadgeClass("rejected")).toBe("badge badge-error");
    expect(keyRequestStatusBadgeClass("cancelled")).toBe("badge badge-ghost");
  });
});

describe("formatRequestLimit", () => {
  it("formats cents as dollars and dashes zero", () => {
    expect(formatRequestLimit(150000)).toBe("$1500.00");
    expect(formatRequestLimit(0)).toBe("—");
    expect(formatRequestLimit(undefined)).toBe("—");
  });
});
