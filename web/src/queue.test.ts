import { describe, expect, it } from "vitest";
import { queueItemText, queueKindBadge, sortByRank, type QueueItem } from "./queue";

function item(overrides: Partial<QueueItem> = {}): QueueItem {
  return {
    initiative_id: "i1",
    short_title: "Add CSV export",
    kind: "decision",
    label: "Which database?",
    rank: 1,
    href: "#/i/i1/define",
    ...overrides,
  };
}

describe("sortByRank", () => {
  it("orders ascending -- rank 1 first", () => {
    const items = [item({ rank: 3 }), item({ rank: 1 }), item({ rank: 2 })];
    expect(sortByRank(items).map((i) => i.rank)).toEqual([1, 2, 3]);
  });

  it("is a no-op on an already-sorted list", () => {
    const items = [item({ rank: 1 }), item({ rank: 2 })];
    expect(sortByRank(items)).toEqual(items);
  });

  it("keeps original relative order for equal ranks (stable)", () => {
    const a = item({ rank: 1, label: "first" });
    const b = item({ rank: 1, label: "second" });
    expect(sortByRank([a, b]).map((i) => i.label)).toEqual(["first", "second"]);
  });

  it("does not mutate the input array", () => {
    const items = [item({ rank: 2 }), item({ rank: 1 })];
    const copy = [...items];
    sortByRank(items);
    expect(items).toEqual(copy);
  });

  it("handles an empty list", () => {
    expect(sortByRank([])).toEqual([]);
  });
});

describe("queueItemText", () => {
  it("joins short title and label", () => {
    expect(queueItemText(item())).toBe("Add CSV export — Which database?");
  });
});

describe("queueKindBadge", () => {
  it("title-cases a known kind", () => {
    expect(queueKindBadge("decision")).toBe("Decision");
    expect(queueKindBadge("question")).toBe("Question");
    expect(queueKindBadge("finding")).toBe("Finding");
  });

  it("still renders an unrecognized kind rather than dropping it", () => {
    expect(queueKindBadge("stale")).toBe("Stale");
  });

  it("falls back to a generic label for an empty kind", () => {
    expect(queueKindBadge("")).toBe("Item");
  });
});
