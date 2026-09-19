import { describe, expect, it } from "vitest";
import {
  decisionCodesFromReasons,
  staleBannerText,
  staleItemHref,
  staleItemLabel,
  type StaleItemRef,
} from "./staleItems";

function item(overrides: Partial<StaleItemRef> = {}): StaleItemRef {
  return {
    stale_id: "stale-1",
    type: "requirement",
    id: "req-1",
    label: "REQ-3",
    reason: "DEC-9 changed",
    ...overrides,
  };
}

describe("decisionCodesFromReasons", () => {
  it("extracts a single code", () => {
    expect(decisionCodesFromReasons([item()])).toEqual(["DEC-9"]);
  });

  it("dedupes repeated codes across items", () => {
    const items = [item({ reason: "DEC-9 changed" }), item({ reason: "DEC-9 changed" })];
    expect(decisionCodesFromReasons(items)).toEqual(["DEC-9"]);
  });

  it("keeps first-seen order across distinct codes", () => {
    const items = [item({ reason: "DEC-11 changed" }), item({ reason: "DEC-9 changed" })];
    expect(decisionCodesFromReasons(items)).toEqual(["DEC-11", "DEC-9"]);
  });

  it("skips a reason that doesn't match the '<CODE> changed' shape", () => {
    expect(decisionCodesFromReasons([item({ reason: "something else entirely" })])).toEqual([]);
  });

  it("returns an empty array for no items", () => {
    expect(decisionCodesFromReasons([])).toEqual([]);
  });
});

describe("staleBannerText", () => {
  it("singular for one item, one decision", () => {
    expect(staleBannerText([item()])).toBe("1 item may be invalid since DEC-9 changed");
  });

  it("plural for multiple items, one decision", () => {
    const items = [item(), item({ stale_id: "stale-2", label: "REQ-4" })];
    expect(staleBannerText(items)).toBe("2 items may be invalid since DEC-9 changed");
  });

  it("joins two distinct decisions with 'and'", () => {
    const items = [item({ reason: "DEC-9 changed" }), item({ reason: "DEC-11 changed" })];
    expect(staleBannerText(items)).toBe("2 items may be invalid since DEC-9 and DEC-11 changed");
  });

  it("joins three or more decisions with an Oxford comma", () => {
    const items = [
      item({ reason: "DEC-9 changed" }),
      item({ reason: "DEC-11 changed" }),
      item({ reason: "DEC-14 changed" }),
    ];
    expect(staleBannerText(items)).toBe("3 items may be invalid since DEC-9, DEC-11, and DEC-14 changed");
  });

  it("falls back to a decision-free sentence when nothing parses, rather than showing nothing", () => {
    expect(staleBannerText([item({ reason: "some future reason shape" })])).toBe(
      "1 item may be invalid",
    );
  });
});

describe("staleItemLabel", () => {
  it("uses the human code when present", () => {
    expect(staleItemLabel(item())).toBe("REQ-3 — DEC-9 changed");
  });

  it("falls back to the bare type when the node has no resolvable label", () => {
    expect(staleItemLabel(item({ label: undefined }))).toBe("requirement — DEC-9 changed");
  });
});

describe("staleItemHref", () => {
  it("points a requirement at Verify", () => {
    expect(staleItemHref("i1", "requirement")).toBe("#/i/i1/verify");
  });

  it("points a task at Plan", () => {
    expect(staleItemHref("i1", "task")).toBe("#/i/i1/plan");
  });

  it("points a change_set at Build", () => {
    expect(staleItemHref("i1", "change_set")).toBe("#/i/i1/build");
  });

  it("returns empty for an unrecognized type rather than a broken link", () => {
    expect(staleItemHref("i1", "widget")).toBe("");
  });
});
