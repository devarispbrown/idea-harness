import { describe, expect, it } from "vitest";
import {
  evalFormFrom,
  evalPutBody,
  lastRunText,
  nextRunText,
  relative,
  runCost,
  validateEvalForm,
  type Automation,
} from "./automations";

const now = new Date("2026-10-14T12:00:00Z");
const job: Automation = {
  name: "eval",
  label: "Scheduled eval",
  description: "",
  enabled: true,
  available: true,
  schedule: "every 24h",
  running: false,
  last_run: null,
  next_run_at: "2026-10-14T15:00:00Z",
  history: [],
};

describe("automation run lines", () => {
  it("says off when off, and when it next runs when on", () => {
    expect(nextRunText({ ...job, enabled: false }, now)).toMatch(/^Off/);
    expect(nextRunText(job, now)).toBe("Next in 3h");
    expect(nextRunText({ ...job, next_run_at: "2026-10-14T11:00:00Z" }, now)).toBe("Due now");
    expect(nextRunText({ ...job, running: true }, now)).toBe("Running now");
  });

  it("reports the last run and its cost", () => {
    expect(lastRunText(job, now)).toBe("Never run");
    const run = { id: "r", automation: "eval", trigger: "manual" as const, status: "failed" as const, started_at_ms: now.getTime() - 20 * 60000 };
    expect(lastRunText({ ...job, last_run: run }, now)).toBe("Last failed 20m ago");
    expect(runCost(run)).toBe("—");
    expect(runCost({ ...run, cost_usd: 0.42 })).toBe("$0.42");
    expect(runCost({ ...run, cost_usd: 0 })).toBe("$0");
  });

  it("renders relative times both ways", () => {
    expect(relative(new Date(now.getTime() + 10_000), now)).toBe("just now");
    expect(relative(new Date(now.getTime() - 3 * 86400_000), now)).toBe("3d ago");
  });
});

describe("eval settings form", () => {
  it("requires a model and a cost cap before it can be enabled", () => {
    const e = validateEvalForm(evalFormFrom(undefined));
    expect(e.model).toMatch(/provider\/model/);
    expect(e.maxCostUSD).toMatch(/Required/);
    expect(validateEvalForm({ ...evalFormFrom(undefined), model: "openai/gpt-4o-mini", maxCostUSD: "2", intervalHours: "0" }).intervalHours).toBeDefined();
    expect(validateEvalForm({ ...evalFormFrom(undefined), model: "openai/gpt-4o-mini", maxCostUSD: "2", baseline: "../x.json" }).baseline).toBeDefined();
    expect(validateEvalForm({ ...evalFormFrom(undefined), model: "openai/gpt-4o-mini", maxCostUSD: "2" })).toEqual({});
  });

  it("builds the PUT body", () => {
    const f = { ...evalFormFrom(undefined), model: " openai/gpt-4o-mini ", maxCostUSD: "1.5", cases: "todo-cli, notes" };
    expect(evalPutBody(f)).toEqual({ model: "openai/gpt-4o-mini", max_cost_usd: 1.5, interval_hours: 0, suite: "", cases: ["todo-cli", "notes"], baseline: "" });
  });
});
