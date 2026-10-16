import { describe, expect, it } from "vitest";
import { agentCaveat, capInputError, meterFraction, meterLabel, meterTone, raiseHint, showInShell, type BudgetStatus } from "./budget";

const base: BudgetStatus = {
  cap_usd: 5,
  source: "project",
  initiative_cap_usd: null,
  project_default_usd: 5,
  spent_usd: 1,
  model_usd: 1,
  agent_usd: null,
  agent_runs: 0,
  agent_not_counted: false,
  remaining_usd: 4,
  exceeded: false,
  parked: false,
  note: "",
};

describe("budget meter", () => {
  it("fills toward the cap and turns at 80% and at the cap", () => {
    expect(meterFraction(base)).toBeCloseTo(0.2);
    expect(meterTone(base)).toBe("ok");
    expect(meterTone({ ...base, spent_usd: 4.2 })).toBe("warn");
    expect(meterTone({ ...base, spent_usd: 5, exceeded: true })).toBe("over");
    expect(meterFraction({ ...base, spent_usd: 9 })).toBe(1);
  });

  it("says where the cap comes from, and when there is none", () => {
    expect(meterLabel(base)).toBe("$1.00 of $5.00 (project default)");
    expect(meterLabel({ ...base, source: "initiative" })).toMatch(/this initiative's cap/);
    expect(meterLabel({ ...base, cap_usd: null, source: "none", spent_usd: 0.0012 })).toBe("$0.0012 spent · no cap");
  });

  it("always states unmeasured agent spend", () => {
    expect(agentCaveat(base)).toBe("");
    expect(agentCaveat({ ...base, agent_runs: 2, agent_not_counted: true })).toMatch(/not counted/);
  });

  it("shows in the shell only with a cap or a stop", () => {
    expect(showInShell(null)).toBe(false);
    expect(showInShell({ ...base, cap_usd: null, source: "none" })).toBe(false);
    expect(showInShell(base)).toBe(true);
    expect(showInShell({ ...base, cap_usd: null, parked: true })).toBe(true);
  });
});

describe("cap inputs", () => {
  it("validates a typed amount", () => {
    expect(capInputError("", false)).toMatch(/Enter/);
    expect(capInputError("abc", false)).toMatch(/dollar/);
    expect(capInputError("-2", true)).toMatch(/dollar/);
    expect(capInputError("0", false)).toMatch(/more than \$0/);
    expect(capInputError("0", true)).toBe("");
    expect(capInputError("$2.50", false)).toBe("");
  });

  it("suggests a cap above the spend", () => {
    expect(raiseHint({ ...base, spent_usd: 5.2, exceeded: true })).toBe(11);
    expect(raiseHint({ ...base, cap_usd: 0.15, spent_usd: 0.2 })).toBe(2);
  });
});
