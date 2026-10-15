import { describe, expect, it } from "vitest";
import { revisionOutcome } from "./planRevision";
import { acceptableFindingCount, reviewRequestBody } from "./buildLogic";
import { changeSetChip, isPlaceholderChange } from "./statusChips";
import { providerListLabel } from "./providerStatus";
import { executorResolutionLine, executorStatusLines, hasExecutorStatusLines, requestedExecutorKind } from "./executorStatus";
import { resolveInitialProvider } from "./settingsLoad";

describe("revisionOutcome", () => {
  it("says a revision that changed nothing changed nothing", () => {
    expect(revisionOutcome({ version: 1, codes: ["TASK-1"] }, { version: 1, codes: ["TASK-1"] })).toBe(
      "The revision produced the same plan: still v1, tasks unchanged.",
    );
  });
  it("names the new version and the task delta", () => {
    expect(revisionOutcome({ version: 1, codes: ["TASK-1"] }, { version: 2, codes: ["TASK-1", "TASK-2"] })).toBe(
      "Plan revised: v1 → v2 (added TASK-2).",
    );
  });
});

describe("acceptableFindingCount", () => {
  const f = { severity: "high" };
  it("counts only open, blocking, reviewer-raised, non-advisory findings", () => {
    expect(
      acceptableFindingCount([
        f,
        { severity: "critical" },
        { severity: "observation" },
        { severity: "medium" },
        { ...f, disposition: "accepted_risk" },
        { ...f, code: "secret_sweep" },
        { ...f, advisory: true },
      ]),
    ).toBe(2);
    expect(acceptableFindingCount([])).toBe(0);
  });
});

describe("reviewRequestBody", () => {
  it("carries the note only with the verdicts that rerun the task", () => {
    expect(reviewRequestBody("changes_requested", false, "  rename it ")).toEqual({
      verdict: "changes_requested",
      note: "rename it",
    });
    expect(reviewRequestBody("rejected", false, "no")).toEqual({ verdict: "rejected", note: "no" });
    expect(reviewRequestBody("approved", true, "ignored")).toEqual({ verdict: "approved", accept_findings: true });
    expect(reviewRequestBody("changes_requested", false, "   ")).toEqual({ verdict: "changes_requested" });
  });
});

describe("changeSetChip", () => {
  it("keeps the status as the label and puts caveats in the marker", () => {
    expect(changeSetChip({ status: "applied", model: "demo" })).toMatchObject({ label: "applied", marker: "demo" });
    expect(changeSetChip({ status: "applied", executor: "hermes", model: "anthropic/x" })).toEqual({
      label: "applied",
      state: "verified",
    });
    expect(changeSetChip({ status: "applied", no_op: true, executor: "internal" }).marker).toBe("no file changes");
    expect(changeSetChip({ status: "applied", executor: "hermes" }, true).marker).toBe("no changes");
    expect(changeSetChip({ status: "review_required", executor: "internal" })).toMatchObject({
      label: "review required",
      marker: "demo",
    });
    expect(changeSetChip({ status: "something_new" }).label).toBe("something new");
  });
  it("treats a change set with no recorded executor by its model alone", () => {
    expect(isPlaceholderChange({ model: "demo" })).toBe(true);
    expect(isPlaceholderChange({ model: "anthropic/x" })).toBe(false);
  });
});

describe("providerListLabel", () => {
  const ollama = { ready: true, key_set: true, configured: false, key_env: "", is_default: false };
  it("says a ready provider is not selected while the project is in demo mode", () => {
    expect(providerListLabel(ollama, true)).toBe("ready · not selected");
    expect(providerListLabel(ollama, false)).toBe("ready");
    expect(providerListLabel({ ...ollama, is_default: true }, true)).toBe("ready");
    expect(providerListLabel({ ...ollama, ready: false, key_env: "X" }, true)).toBe("not configured");
  });
});

describe("executor requested vs resolved", () => {
  const res = { requested: "auto", resolved: "internal" };
  it("reads the requested kind from the resolution when nothing is selected", () => {
    expect(requestedExecutorKind([{ name: "internal", selected: false, resolution: res }])).toBe("auto");
    expect(requestedExecutorKind([{ name: "hermes", selected: true }])).toBe("hermes");
    expect(requestedExecutorKind([])).toBe("auto");
  });
  it("says what auto resolves to", () => {
    expect(executorResolutionLine(res)).toBe("Requested: auto · resolves to: internal.");
    expect(executorResolutionLine({ requested: "hermes", resolved: "hermes" })).toBe("Runs: hermes.");
    expect(executorResolutionLine(undefined)).toBe("");
  });
  it("has nothing to say about an image for the internal executor", () => {
    const lines = executorStatusLines({ image: "", image_present: true, key_env: "", key_set: true, problem: "" });
    expect(lines.imageLine).toBeUndefined();
    expect(hasExecutorStatusLines(lines)).toBe(false);
  });
});

describe("resolveInitialProvider with providers", () => {
  it("prefers a ready provider over an unconfigured openai", () => {
    expect(resolveInitialProvider("", [{ name: "openai", ready: false }, { name: "ollama", ready: true }])).toBe("ollama");
    expect(resolveInitialProvider("", [{ name: "openai", ready: false }])).toBe("openai");
  });
});
