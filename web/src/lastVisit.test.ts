import { describe, expect, it } from "vitest";
import { entriesSince, getLastVisit, setLastVisit } from "./lastVisit";
import type { TimelineEntry } from "./timeline";

function entry(at: string, title = "entry"): TimelineEntry {
  return { at, kind: "change_set_applied", title };
}

describe("entriesSince", () => {
  const entries = [entry("2026-09-18T12:00:00Z", "c"), entry("2026-09-17T12:00:00Z", "b"), entry("2026-09-16T12:00:00Z", "a")];

  it("keeps only entries strictly newer than the cursor", () => {
    const kept = entriesSince(entries, "2026-09-17T00:00:00Z");
    expect(kept.map((e) => e.title)).toEqual(["c", "b"]);
  });

  it("excludes an entry exactly at the cursor (strictly newer, not >=)", () => {
    const kept = entriesSince(entries, "2026-09-17T12:00:00Z");
    expect(kept.map((e) => e.title)).toEqual(["c"]);
  });

  it("returns nothing for a null cursor (first-ever visit) rather than everything", () => {
    expect(entriesSince(entries, null)).toEqual([]);
  });

  it("returns nothing for an unparseable cursor", () => {
    expect(entriesSince(entries, "not-a-date")).toEqual([]);
  });

  it("returns everything when the cursor predates all entries", () => {
    const kept = entriesSince(entries, "2000-01-01T00:00:00Z");
    expect(kept).toHaveLength(3);
  });

  it("returns nothing for an empty entries list", () => {
    expect(entriesSince([], "2000-01-01T00:00:00Z")).toEqual([]);
  });
});

// This suite runs under vitest's default "node" environment (no jsdom, no
// `window`, per api.ts's own module-level guard for the same reason) -- the
// point of these two is exactly that a missing/broken storage accessor must
// degrade quietly instead of throwing into the caller.
describe("getLastVisit / setLastVisit without a window", () => {
  it("getLastVisit returns null instead of throwing", () => {
    expect(() => getLastVisit("init-1")).not.toThrow();
    expect(getLastVisit("init-1")).toBeNull();
  });

  it("setLastVisit does not throw", () => {
    expect(() => setLastVisit("init-1", "2026-09-18T12:00:00Z")).not.toThrow();
  });
});
