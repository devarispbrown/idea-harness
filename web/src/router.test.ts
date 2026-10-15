import { describe, expect, it } from "vitest";
import { lineageHref, parseRoute } from "./router";

// K: a bogus route used to silently resolve to home, indistinguishable from
// the user having navigated there on purpose. parseRoute now returns a
// distinct "not-found" route for anything it doesn't recognize, and the
// literal home/empty-hash cases are pinned down alongside it so the fix
// can't regress into flagging real navigation as not-found.
describe("parseRoute", () => {
  it("resolves the empty hash to home", () => {
    expect(parseRoute("#/")).toEqual({ name: "home" });
    expect(parseRoute("")).toEqual({ name: "home" });
  });

  it("resolves automations", () => {
    expect(parseRoute("#/automations")).toEqual({ name: "automations" });
    expect(parseRoute("#/automations/eval").name).toBe("not-found");
  });

  it("resolves settings", () => {
    expect(parseRoute("#/settings")).toEqual({ name: "settings" });
  });

  it("resolves a phase route", () => {
    expect(parseRoute("#/i/abc/explore")).toEqual({ name: "phase", id: "abc", phase: "explore" });
  });

  it("resolves decision, health, doc and diff routes", () => {
    expect(parseRoute("#/i/abc/decision/d1")).toEqual({ name: "decision", id: "abc", decisionId: "d1" });
    expect(parseRoute("#/i/abc/health")).toEqual({ name: "health", id: "abc" });
    expect(parseRoute("#/i/abc/doc/art1/3")).toEqual({
      name: "doc",
      id: "abc",
      artifact: "art1",
      version: "3",
    });
    expect(parseRoute("#/i/abc/doc/art1")).toEqual({
      name: "doc",
      id: "abc",
      artifact: "art1",
      version: "latest",
    });
    expect(parseRoute("#/i/abc/diff/v1/v2")).toEqual({ name: "diff", id: "abc", from: "v1", to: "v2" });
  });

  it("resolves the queue route (D-b, wave 3)", () => {
    expect(parseRoute("#/queue")).toEqual({ name: "queue" });
  });

  it("resolves the per-initiative changed route (C1, wave 3)", () => {
    expect(parseRoute("#/i/abc/changed")).toEqual({ name: "changed", id: "abc" });
  });

  it("does not silently fall back to home for a bogus top-level path", () => {
    expect(parseRoute("#/nope").name).toBe("not-found");
  });

  it("does not silently fall back to home for a bogus phase name", () => {
    expect(parseRoute("#/i/abc/not-a-phase").name).toBe("not-found");
  });

  it("does not silently fall back to home for a malformed initiative path", () => {
    expect(parseRoute("#/i/abc").name).toBe("not-found");
  });

  it("keeps the offending path for display", () => {
    expect(parseRoute("#/weird/path")).toEqual({ name: "not-found", path: "weird/path" });
  });
});

describe("lineage route", () => {
  it("parses #/i/<id>/lineage/<type>/<ref>", () => {
    expect(parseRoute("#/i/abc/lineage/context_doc/doc-1")).toEqual({
      name: "lineage",
      id: "abc",
      nodeType: "context_doc",
      ref: "doc-1",
    });
  });

  it("round-trips through lineageHref, encoding what needs it", () => {
    const href = lineageHref("abc", "requirement", "REQ 1/x");
    expect(parseRoute(href)).toEqual({ name: "lineage", id: "abc", nodeType: "requirement", ref: "REQ 1/x" });
  });

  it("is not-found with a missing ref", () => {
    expect(parseRoute("#/i/abc/lineage/context_doc").name).toBe("not-found");
  });
});
