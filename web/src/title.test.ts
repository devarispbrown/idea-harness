import { describe, expect, it } from "vitest";
import { displayTitle } from "./title";

describe("displayTitle", () => {
  it("prefers short_title when present", () => {
    expect(displayTitle({ short_title: "Add CSV export", title: "As a user I want to..." })).toBe(
      "Add CSV export",
    );
  });

  it("falls back to title when short_title is absent (pre-wave-3 daemon)", () => {
    expect(displayTitle({ title: "The full 600-char prompt" })).toBe("The full 600-char prompt");
  });

  it("falls back to title when short_title is an empty string", () => {
    expect(displayTitle({ short_title: "", title: "Full title" })).toBe("Full title");
  });

  // Mutation guard: swapping `||` for `&&` would return the empty string
  // (falsy) here instead of falling through to title.
  it("does not return an empty string when short_title is falsy", () => {
    expect(displayTitle({ short_title: "", title: "Full title" })).not.toBe("");
  });
});
