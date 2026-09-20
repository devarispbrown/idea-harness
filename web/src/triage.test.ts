import { describe, expect, it } from "vitest";
import { groupTriage, optionalLabel, partitionByTriage, type TriageItemLike } from "./triage";

function item(id: string, rank: "priority" | "optional"): TriageItemLike {
  return { id, rank };
}

describe("groupTriage", () => {
  it("keeps up to 3 priority items as needsYou", () => {
    const items = [item("a", "priority"), item("b", "priority"), item("c", "priority")];
    const { needsYou, optional } = groupTriage(items);
    expect(needsYou.map((i) => i.id)).toEqual(["a", "b", "c"]);
    expect(optional).toEqual([]);
  });

  it("caps needsYou at 3 and pushes overflow into optional, ahead of the already-optional items", () => {
    const items = [
      item("a", "priority"),
      item("b", "priority"),
      item("c", "priority"),
      item("d", "priority"), // overflow: backend somehow marked a 4th priority
      item("e", "optional"),
    ];
    const { needsYou, optional } = groupTriage(items);
    expect(needsYou.map((i) => i.id)).toEqual(["a", "b", "c"]);
    expect(optional.map((i) => i.id)).toEqual(["d", "e"]);
  });

  it("needsYou is empty when nothing is marked priority", () => {
    const items = [item("a", "optional"), item("b", "optional")];
    const { needsYou, optional } = groupTriage(items);
    expect(needsYou).toEqual([]);
    expect(optional).toHaveLength(2);
  });

  it("handles an empty list", () => {
    expect(groupTriage([])).toEqual({ needsYou: [], optional: [] });
  });
});

describe("partitionByTriage", () => {
  interface Open {
    id: string;
    label: string;
  }
  const items: Open[] = [
    { id: "d1", label: "decision 1" },
    { id: "f1", label: "finding 1" },
    { id: "q1", label: "question 1" },
  ];

  it("puts items triage marked priority into needsYou", () => {
    const triage = [item("d1", "priority"), item("f1", "optional"), item("q1", "optional")];
    const { needsYou, optional } = partitionByTriage(items, triage);
    expect(needsYou.map((i) => i.id)).toEqual(["d1"]);
    expect(optional.map((i) => i.id)).toEqual(["f1", "q1"]);
  });

  it("preserves the input order within each bucket", () => {
    const triage = [item("d1", "priority"), item("f1", "priority"), item("q1", "optional")];
    const { needsYou } = partitionByTriage(items, triage);
    expect(needsYou.map((i) => i.id)).toEqual(["d1", "f1"]);
  });

  it("fails open: an item triage never mentions goes to optional, not lost", () => {
    const { needsYou, optional } = partitionByTriage(items, []);
    expect(needsYou).toEqual([]);
    expect(optional).toHaveLength(3);
  });

  it("every input item appears in exactly one bucket", () => {
    const triage = [item("d1", "priority")];
    const { needsYou, optional } = partitionByTriage(items, triage);
    expect(needsYou.length + optional.length).toBe(items.length);
  });

  // LOW fix (release-gate report): partitionByTriage used to trust every
  // `priority`-ranked triage row unconditionally -- groupTriage's own
  // defensive top-3 cap had no production caller. A backend that ever marks
  // a 4th item priority must not grow this page's "needs you" section past 3.
  it("caps needsYou at 3, same as groupTriage, even with a 4th item all four callers agree is priority", () => {
    const fourItems: Open[] = [
      ...items,
      { id: "q2", label: "question 2" },
    ];
    const triage = [
      item("d1", "priority"),
      item("f1", "priority"),
      item("q1", "priority"),
      item("q2", "priority"), // overflow: backend somehow marked a 4th priority
    ];
    const { needsYou, optional } = partitionByTriage(fourItems, triage);
    expect(needsYou.map((i) => i.id)).toEqual(["d1", "f1", "q1"]);
    expect(optional.map((i) => i.id)).toEqual(["q2"]);
  });
});

describe("optionalLabel", () => {
  it("includes the count", () => {
    expect(optionalLabel(5)).toBe("Optional — the recommendation will be used (5)");
  });

  it("still renders for 0", () => {
    expect(optionalLabel(0)).toBe("Optional — the recommendation will be used (0)");
  });
});
