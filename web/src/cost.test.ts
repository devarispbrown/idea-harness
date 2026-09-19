import { describe, expect, it } from "vitest";
import {
  costLine,
  formatDuration,
  formatUsd,
  phaseBreakdownLines,
  totalPhaseSeconds,
  type CostsView,
} from "./cost";

describe("formatUsd", () => {
  it("formats a normal amount to 2 decimals", () => {
    expect(formatUsd(1.4)).toBe("$1.40");
    expect(formatUsd(12.345)).toBe("$12.35");
  });

  it("formats zero", () => {
    expect(formatUsd(0)).toBe("$0.00");
  });

  it("shows 3 decimals for sub-cent spend so it doesn't read as free", () => {
    expect(formatUsd(0.004)).toBe("$0.004");
  });

  it("says not measured for null", () => {
    expect(formatUsd(null)).toBe("not measured");
  });

  it("says not measured for NaN/non-finite", () => {
    expect(formatUsd(NaN)).toBe("not measured");
    expect(formatUsd(Infinity)).toBe("not measured");
  });
});

describe("formatDuration", () => {
  it("shows seconds under a minute", () => {
    expect(formatDuration(0)).toBe("0s");
    expect(formatDuration(45)).toBe("45s");
    expect(formatDuration(59)).toBe("59s");
  });

  it("shows minutes and seconds under an hour", () => {
    expect(formatDuration(60)).toBe("1m");
    expect(formatDuration(192)).toBe("3m 12s");
    expect(formatDuration(3599)).toBe("59m 59s");
  });

  it("shows hours and zero-padded minutes at/over an hour", () => {
    expect(formatDuration(3600)).toBe("1h 00m");
    expect(formatDuration(3900)).toBe("1h 05m");
    expect(formatDuration(7530)).toBe("2h 05m");
  });

  it("clamps a negative or missing value to 0s rather than throwing", () => {
    expect(formatDuration(-5)).toBe("0s");
    expect(formatDuration(undefined as unknown as number)).toBe("0s");
  });
});

describe("totalPhaseSeconds", () => {
  it("sums seconds across phases", () => {
    expect(
      totalPhaseSeconds([
        { phase: "define", usd: 0, seconds: 10, calls: 1 },
        { phase: "plan", usd: 0, seconds: 20, calls: 1 },
      ]),
    ).toBe(30);
  });

  it("is 0 for an empty list", () => {
    expect(totalPhaseSeconds([])).toBe(0);
  });
});

function costs(overrides: Partial<CostsView> = {}): CostsView {
  return {
    total_usd: 1.5,
    by_phase: [{ phase: "define", usd: 1.5, seconds: 90, calls: 3 }],
    agent: { runs: 0, seconds: 0, usd: null },
    ...overrides,
  };
}

describe("costLine", () => {
  it("shows money and time, no agent segment when there are no agent runs", () => {
    expect(costLine(costs())).toBe("$1.50 · 1m 30s");
  });

  // The time figure includes the agent's own seconds (review Low fix,
  // matching `idea status`'s cliCosts.summary()): this used to sum only
  // by_phase, so the same initiative showed a shorter elapsed time on the
  // web than at the CLI. by_phase's 90s + the agent's 300s = 390s = 6m 30s.
  it("adds the agent segment once there is at least one agent run, and folds the agent's seconds into the time figure", () => {
    const line = costLine(costs({ agent: { runs: 2, seconds: 300, usd: 0.8 } }));
    expect(line).toBe("$1.50 · 6m 30s · agent: $0.80");
  });

  it("shows 'not measured' for the agent segment when usd is null but runs exist", () => {
    const line = costLine(costs({ agent: { runs: 1, seconds: 10, usd: null } }));
    expect(line).toContain("agent: not measured");
  });
});

describe("phaseBreakdownLines", () => {
  it("lists a phase that spent something", () => {
    const lines = phaseBreakdownLines([{ phase: "plan", usd: 0.1, seconds: 5, calls: 1 }]);
    expect(lines).toEqual(["plan: $0.10 · 5s · 1 call"]);
  });

  it("pluralizes calls", () => {
    const lines = phaseBreakdownLines([{ phase: "plan", usd: 0.1, seconds: 5, calls: 2 }]);
    expect(lines[0]).toContain("2 calls");
  });

  it("drops a phase with no calls, seconds or spend", () => {
    expect(phaseBreakdownLines([{ phase: "explore", usd: 0, seconds: 0, calls: 0 }])).toEqual([]);
  });
});
