// @vitest-environment jsdom
//
// The Define page's metric edit form covers every field the metric edit API
// takes -- guardrail, cadence, definition, source and objective as well as
// name/direction/baseline/target/unit -- and refuses what the API would.
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import ObjectivesSection from "./ObjectivesSection";
import { fakeDaemon } from "./testing/fakeDaemon";
import { cadenceError, metricDraft, metricEditBody } from "./outcomes";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const m = {
  name: "Shares",
  direction: "increase" as const,
  target: 50,
  baseline: 10,
  unit: "",
  guardrail: 5,
  cadence: "weekly",
  definition: "shares per week",
  objective_code: "OBJ-1",
  source: { kind: "manual" as const },
};

describe("metricEditBody: every field", () => {
  it("sends nothing for an untouched draft", () => {
    expect(metricEditBody(m, metricDraft(m))).toEqual({ body: {} });
  });

  it("clears a guardrail emptied in the form, and sets changed cadence, definition and objective", () => {
    expect(
      metricEditBody(m, { ...metricDraft(m), guardrail: "", cadence: " 36h ", definition: "shares / week", objective: "OBJ-2" }),
    ).toEqual({ body: { clear_guardrail: true, cadence: "36h", definition: "shares / week", objective: "OBJ-2" } });
    expect(metricEditBody(m, { ...metricDraft(m), guardrail: "8" })).toEqual({ body: { guardrail: 8 } });
  });

  it("sends a changed source whole", () => {
    expect(metricEditBody(m, { ...metricDraft(m), sourceKind: "command", command: " wc -l < shares.log " })).toEqual({
      body: { source: { kind: "command", command: "wc -l < shares.log" } },
    });
    expect(
      metricEditBody(m, { ...metricDraft(m), sourceKind: "url", url: "https://stats.example/api", pointer: "/data/shares" }),
    ).toEqual({ body: { source: { kind: "url", url: "https://stats.example/api", pointer: "/data/shares" } } });
    // Unchanged url source: nothing sent.
    const u = { ...m, source: { kind: "url" as const, url: "https://x.example/n" } };
    expect(metricEditBody(u, metricDraft(u))).toEqual({ body: {} });
  });

  it("refuses what the API refuses", () => {
    const d = metricDraft(m);
    expect(metricEditBody(m, { ...d, guardrail: "low" })).toEqual({ error: "Guardrail must be a number (or blank)." });
    expect(metricEditBody(m, { ...d, direction: "maintain", guardrail: "-1" })).toEqual({
      error: "A maintain metric's guardrail is the allowed distance from the target and cannot be negative.",
    });
    expect(metricEditBody(m, { ...d, cadence: "fortnightly" })).toHaveProperty("error");
    expect(metricEditBody(m, { ...d, sourceKind: "command", command: "  " })).toEqual({ error: "A command source needs a command." });
    expect(metricEditBody(m, { ...d, sourceKind: "url", url: "ftp://x" })).toEqual({ error: "A URL source needs an http(s) URL." });
    expect(metricEditBody(m, { ...d, sourceKind: "url", url: "https://x.example", pointer: "data" })).toEqual({
      error: 'Pointer "data" is not a JSON pointer (it must start with /).',
    });
  });

  it("parses cadences as domain.ParseCadence does", () => {
    for (const ok of ["", "daily", "Weekly", "quarterly", "3d", "2w", "36h", "90m", "1h30m", "1.5h"]) {
      expect(cadenceError(ok), ok).toBeUndefined();
    }
    for (const bad of ["fortnightly", "0d", "0h", "-2h", "3 days", "d", "h"]) {
      expect(cadenceError(bad), bad).toBeDefined();
    }
  });
});

describe("ObjectivesSection metric edit form", () => {
  const P = "/api/v1/projects/p1/initiatives/i1";
  const objectives = ["OBJ-1", "OBJ-2"].map((code, i) => ({
    id: `o${i + 1}`,
    code,
    statement: `Objective ${i + 1}`,
    rationale: "",
    status: "active",
    requirement_ids: [],
    metric_ids: i === 0 ? ["m1"] : [],
  }));
  const metric = {
    id: "m1",
    code: "MET-1",
    objective_id: "o1",
    objective_code: "OBJ-1",
    name: "Shares per week",
    direction: "increase",
    baseline: 10,
    target: 50,
    source: { kind: "manual" },
    status: { drift: "no_data" },
  };
  const daemon = () =>
    fakeDaemon({
      [`GET ${P}/objectives`]: objectives,
      [`GET ${P}/metrics`]: [metric],
      [`GET ${P}/requirements`]: [],
      [`PATCH ${P}/metrics/MET-1`]: metric,
    });

  it("edits guardrail, cadence, definition, source and objective", async () => {
    const { requests } = daemon();
    render(<ObjectivesSection projectId="p1" initiativeId="i1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit MET-1" }));
    fireEvent.change(screen.getByLabelText("MET-1 guardrail (blank for none)"), { target: { value: "8" } });
    fireEvent.change(screen.getByLabelText("MET-1 cadence (blank for none)"), { target: { value: "daily" } });
    fireEvent.change(screen.getByLabelText("MET-1 definition"), { target: { value: "shares in 7 days" } });
    fireEvent.change(screen.getByLabelText("MET-1 source"), { target: { value: "url" } });
    fireEvent.change(screen.getByLabelText("MET-1 source URL"), { target: { value: "https://stats.example/api" } });
    fireEvent.change(screen.getByLabelText("MET-1 source JSON pointer (blank: the document is the number)"), {
      target: { value: "/shares" },
    });
    fireEvent.change(screen.getByLabelText("MET-1 objective"), { target: { value: "OBJ-2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save MET-1" }));
    await waitFor(() => expect(requests.some((r) => r.method === "PATCH")).toBe(true));
    expect(requests.find((r) => r.method === "PATCH")!.body).toEqual({
      guardrail: 8,
      cadence: "daily",
      definition: "shares in 7 days",
      objective: "OBJ-2",
      source: { kind: "url", url: "https://stats.example/api", pointer: "/shares" },
    });
  });

  it("refuses an invalid cadence before sending anything", async () => {
    const { requests } = daemon();
    render(<ObjectivesSection projectId="p1" initiativeId="i1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit MET-1" }));
    fireEvent.change(screen.getByLabelText("MET-1 cadence (blank for none)"), { target: { value: "fortnightly" } });
    fireEvent.click(screen.getByRole("button", { name: "Save MET-1" }));
    expect(await screen.findByText(/Cadence "fortnightly" is not/)).toBeTruthy();
    expect(requests.some((r) => r.method === "PATCH")).toBe(false);
  });

  it("shows a command field only for a command source", async () => {
    daemon();
    render(<ObjectivesSection projectId="p1" initiativeId="i1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit MET-1" }));
    expect(screen.queryByLabelText("MET-1 source command")).toBeNull();
    fireEvent.change(screen.getByLabelText("MET-1 source"), { target: { value: "command" } });
    expect(screen.getByLabelText("MET-1 source command")).toBeTruthy();
  });
});
