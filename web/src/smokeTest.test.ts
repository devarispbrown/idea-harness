import { describe, expect, it } from "vitest";
import {
  formatSmokeElapsed,
  lastSmokeMessage,
  relativeTime,
  smokeButtonState,
} from "./smokeTest";

describe("smokeButtonState", () => {
  it("is enabled for hermes with no problem", () => {
    expect(smokeButtonState("hermes", { problem: "" })).toEqual({ enabled: true, reason: "" });
  });

  it("is enabled for omp with no problem", () => {
    expect(smokeButtonState("omp", { problem: "" })).toEqual({ enabled: true, reason: "" });
  });

  it("disables and names the reason for any other kind", () => {
    const s = smokeButtonState("internal", { problem: "" });
    expect(s.enabled).toBe(false);
    expect(s.reason).toContain("hermes or omp");
  });

  it("disables and surfaces the executor's own problem", () => {
    const s = smokeButtonState("hermes", { problem: "ANTHROPIC_API_KEY is not set" });
    expect(s.enabled).toBe(false);
    expect(s.reason).toBe("ANTHROPIC_API_KEY is not set");
  });

  // Mutation guard: kind must be checked, not derived only from `problem` --
  // an internal/container executor with an (impossible) non-empty problem
  // must still fail on "select hermes or omp", not report the problem text.
  it("checks kind before problem", () => {
    const s = smokeButtonState("container", { problem: "something" });
    expect(s.reason).toContain("hermes or omp");
  });

  it("disables with no reason before the executors list has loaded", () => {
    expect(smokeButtonState("hermes", undefined)).toEqual({ enabled: false, reason: "" });
  });
});

describe("relativeTime", () => {
  const now = Date.parse("2026-09-18T12:00:00Z");

  it("says just now for anything under a minute old", () => {
    expect(relativeTime("2026-09-18T11:59:31Z", now)).toBe("just now");
    // 45s old: still under the 60s boundary, but past a looser 30s one.
    expect(relativeTime("2026-09-18T11:59:15Z", now)).toBe("just now");
  });

  it("reports whole minutes", () => {
    expect(relativeTime("2026-09-18T11:55:00Z", now)).toBe("5 min ago");
  });

  it("reports whole hours, pluralized", () => {
    expect(relativeTime("2026-09-18T10:00:00Z", now)).toBe("2 hrs ago");
    expect(relativeTime("2026-09-18T11:00:00Z", now)).toBe("1 hr ago");
  });

  it("reports whole days, pluralized", () => {
    expect(relativeTime("2026-09-15T12:00:00Z", now)).toBe("3 days ago");
    expect(relativeTime("2026-09-17T12:00:00Z", now)).toBe("1 day ago");
  });

  it("is empty for an unparsable timestamp rather than 'NaN min ago'", () => {
    expect(relativeTime("not-a-date", now)).toBe("");
  });
});

describe("lastSmokeMessage", () => {
  const now = Date.parse("2026-09-18T12:00:00Z");

  it("is empty when nothing has run yet", () => {
    expect(lastSmokeMessage(undefined, now)).toBe("");
  });

  it("reports a pass with a relative time", () => {
    expect(
      lastSmokeMessage({ status: "succeeded", finished_at: "2026-09-18T11:55:00Z" }, now),
    ).toBe("Last smoke test passed 5 min ago.");
  });

  it("reports a failure with a relative time", () => {
    expect(
      lastSmokeMessage({ status: "failed", finished_at: "2026-09-18T11:55:00Z" }, now),
    ).toBe("Last smoke test failed 5 min ago.");
  });

  it("reports a cancellation distinctly from a failure", () => {
    expect(
      lastSmokeMessage({ status: "cancelled", finished_at: "2026-09-18T11:55:00Z" }, now),
    ).toBe("Last smoke test was cancelled 5 min ago.");
  });

  it("says one is running rather than treating it as a stale pass/fail", () => {
    expect(lastSmokeMessage({ status: "running" }, now)).toBe("A smoke test is running.");
    expect(lastSmokeMessage({ status: "pending" }, now)).toBe("A smoke test is running.");
  });
});

describe("formatSmokeElapsed", () => {
  it("shows seconds under a minute", () => {
    expect(formatSmokeElapsed(45_000)).toBe("45s");
  });

  it("shows minutes and seconds past a minute", () => {
    expect(formatSmokeElapsed(64_000)).toBe("1m 4s");
  });

  it("never shows negative elapsed time", () => {
    expect(formatSmokeElapsed(-500)).toBe("0s");
  });
});
