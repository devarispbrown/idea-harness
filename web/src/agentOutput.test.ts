import { describe, expect, it } from "vitest";
import { shouldStickToBottom } from "./agentOutput";

describe("shouldStickToBottom", () => {
  it("sticks when scrolled exactly to the bottom", () => {
    expect(shouldStickToBottom(88, 100, 12)).toBe(true);
  });

  it("sticks when the content doesn't overflow the box at all", () => {
    // scrollHeight <= clientHeight: nothing to scroll, always "at the bottom".
    expect(shouldStickToBottom(0, 50, 50)).toBe(true);
  });

  it("does not stick once the reader has scrolled meaningfully away from the bottom", () => {
    expect(shouldStickToBottom(0, 500, 100)).toBe(false);
  });

  it("stays sticky within the threshold of the bottom", () => {
    // 400 - (380 + 12) = 8, within the default 24px threshold.
    expect(shouldStickToBottom(380, 400, 12)).toBe(true);
  });

  // Mutation guard: the boundary is inclusive (<=), and the threshold is
  // configurable -- both must actually be honored, not hard-coded away.
  it("honors an explicit threshold", () => {
    expect(shouldStickToBottom(350, 400, 12, 50)).toBe(true);
    expect(shouldStickToBottom(300, 400, 12, 50)).toBe(false);
  });

  it("treats being exactly at the threshold as still sticky (inclusive boundary)", () => {
    // 400 - (376 + 12) = 12, exactly the threshold.
    expect(shouldStickToBottom(376, 400, 12, 12)).toBe(true);
  });
});
