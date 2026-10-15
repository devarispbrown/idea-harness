import { describe, expect, it } from "vitest";
import {
  buildLineageTree,
  directEdges,
  lineageHref,
  nodeDisplay,
  nodeRef,
  normalizeNodeType,
  pageHrefFor,
  resolveRef,
  stalenessCounts,
  uniqueNodes,
  type ImpactEdge,
  type TraceEdge,
} from "./lineage";
import { parseRoute } from "./router";

const e = (from: string, to: string, edge = "derived_from", labels: Record<string, string> = {}): TraceEdge => {
  const [ft, fi] = from.split(":");
  const [tt, ti] = to.split(":");
  return { edge_type: edge, from_type: ft, from_id: fi, from_label: labels[from], to_type: tt, to_id: ti, to_label: labels[to] };
};
const ie = (from: string, to: string, staleness = "revalidate"): ImpactEdge => ({ ...e(from, to, "implements"), staleness });

describe("nodeDisplay / nodeRef", () => {
  it("leads with the code a label starts with", () => {
    expect(nodeDisplay("task", "abc", "TASK-1 Build the core")).toEqual({ code: "TASK-1", text: "Build the core" });
    expect(nodeDisplay("requirement", "abc", "REQ-3")).toEqual({ code: "REQ-3", text: "" });
    expect(nodeRef("task", "abc", "TASK-1 Build the core")).toBe("TASK-1");
  });
  it("falls back to a short id when the label is the daemon's '<type> <id>' placeholder", () => {
    const id = "0123456789abcdef";
    expect(nodeDisplay("question", id, `question ${id}`)).toEqual({ code: "01234567", text: "Question" });
    expect(nodeDisplay("task", id)).toEqual({ code: "01234567", text: "Task" });
    expect(nodeRef("question", id, `question ${id}`)).toBe(id);
  });
  it("reads artifact kinds and assertion statements", () => {
    expect(nodeDisplay("artifact", "0123456789", "prd")).toEqual({ code: "prd", text: "Product requirements" });
    expect(nodeDisplay("assertion", "0123456789", "assumption: users cook").code).toBe("01234567");
  });
});

describe("buildLineageTree", () => {
  it("nests why edges under their source and expands a shared node once", () => {
    // REQ -> OBJ -> RUN, REQ -> RUN (diamond: RUN reached twice)
    const edges = [e("requirement:r", "run:x", "produced_by"), e("requirement:r", "objective:o", "serves"), e("objective:o", "run:x", "produced_by")];
    const tree = buildLineageTree("requirement", "r", edges, "why");
    expect(tree.map((n) => n.key)).toEqual(["run:x", "objective:o"]);
    expect(tree[0].repeat).toBe(false);
    expect(tree[1].children).toHaveLength(1);
    expect(tree[1].children[0]).toMatchObject({ key: "run:x", repeat: true, children: [] });
  });
  it("hangs impact dependents under what they depend on, keeping staleness", () => {
    const edges = [ie("task:t", "requirement:r"), ie("change_set:c", "task:t", "stale")];
    const tree = buildLineageTree("requirement", "r", edges, "impact");
    expect(tree[0]).toMatchObject({ key: "task:t", staleness: "revalidate" });
    expect(tree[0].children[0]).toMatchObject({ key: "change_set:c", staleness: "stale" });
  });
  it("terminates on a cycle and drops duplicate edges", () => {
    const edges = [e("task:a", "task:b", "depends_on"), e("task:b", "task:a", "depends_on"), e("task:a", "task:b", "depends_on")];
    const tree = buildLineageTree("task", "a", edges, "why");
    expect(tree).toHaveLength(1);
    expect(tree[0].children[0]).toMatchObject({ key: "task:a", repeat: true });
  });
  it("tolerates a missing edge list", () => {
    expect(buildLineageTree("task", "a", undefined as never, "why")).toEqual([]);
  });
});

describe("counts", () => {
  const impact = [ie("task:t", "requirement:r"), ie("change_set:c", "task:t", "stale"), ie("change_set:c", "task:t", "revalidate"), ie("metric:m", "change_set:c", "obsolete")];
  it("counts distinct dependents, not edges", () => {
    expect(uniqueNodes(impact, "impact", "requirement:r")).toBe(3);
  });
  it("tallies each dependent once at its worst staleness", () => {
    expect(stalenessCounts(impact)).toEqual({ revalidate: 1, stale: 1, obsolete: 1 });
  });
  it("directEdges keeps the first ring only", () => {
    expect(directEdges(impact, "requirement", "r", "impact")).toHaveLength(1);
  });
});

describe("resolveRef", () => {
  const reqs = [
    { id: "old", code: "REQ-1", statement: "old", status: "superseded" },
    { id: "new", code: "REQ-1", statement: "new", status: "approved" },
    { id: "r2", code: "REQ-2", statement: "two", status: "approved" },
  ];
  it("matches a code case-insensitively and prefers the live row", () => {
    expect(resolveRef("requirement", "req-1", reqs)?.id).toBe("new");
    expect(resolveRef("requirement", "r2", reqs)?.title).toBe("two");
    expect(resolveRef("requirement", "REQ-9", reqs)).toBeNull();
  });
  it("uses an artifact's artifact_id as its graph id and resolves its kind", () => {
    const arts = [{ id: "row1", artifact_id: "graph1", kind: "prd", version: 1 }];
    const n = resolveRef("artifact", "prd", arts);
    expect(n?.id).toBe("graph1");
    expect(pageHrefFor("i1", "artifact", n)).toBe("#/i/i1/doc/row1/1");
  });
  it("accepts an unambiguous id prefix (gaps name assumptions by 8 characters)", () => {
    const as = [{ id: "2eaf3ee8611383e2", statement: "x" }, { id: "e36b0ca354", statement: "y" }];
    expect(resolveRef("assertion", "2eaf3ee8", as)?.id).toBe("2eaf3ee8611383e2");
  });
  it("resolves a quality dimension to its newest result", () => {
    const hist = [
      { id: "q1", dimension: "security", status: "fail" },
      { id: "q2", dimension: "security", status: "pass" },
    ];
    expect(resolveRef("quality_result", "security", hist)?.id).toBe("q2");
  });
});

describe("routing", () => {
  it("normalizes near-miss type spellings", () => {
    expect(normalizeNodeType("change-set")).toBe("change_set");
    expect(normalizeNodeType("requirements")).toBe("requirement");
    expect(normalizeNodeType("bogus")).toBe("bogus");
  });
  it("round-trips a lineage href through the router, slashes included", () => {
    const href = lineageHref("ini", "requirement", "REQ-1/AC-1");
    expect(parseRoute(href)).toEqual({ name: "lineage", id: "ini", nodeType: "requirement", ref: "REQ-1/AC-1" });
    expect(parseRoute("#/i/ini/lineage/task/TASK-2")).toEqual({ name: "lineage", id: "ini", nodeType: "task", ref: "TASK-2" });
    expect(parseRoute("#/i/ini/lineage/task")).toEqual({ name: "not-found", path: "i/ini/lineage/task" });
  });
  it("routes the graph pages", () => {
    expect(parseRoute("#/i/ini/gaps")).toEqual({ name: "gaps", id: "ini" });
    expect(parseRoute("#/i/ini/outcomes")).toEqual({ name: "outcomes", id: "ini" });
    expect(parseRoute("#/i/ini/quality")).toEqual({ name: "quality", id: "ini" });
  });
});
