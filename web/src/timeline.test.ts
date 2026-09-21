import { describe, expect, it } from "vitest";
import {
  describeTimelineEntry,
  groupByDay,
  relativeTime,
  timelineEntryHref,
  timelineKindLabel,
  type TimelineEntry,
} from "./timeline";

function entry(overrides: Partial<TimelineEntry> = {}): TimelineEntry {
  return {
    at: "2026-09-18T12:00:00Z",
    kind: "change_set_applied",
    title: "CHG-1 applied",
    ...overrides,
  };
}

describe("timelineKindLabel", () => {
  it("labels every documented kind", () => {
    expect(timelineKindLabel("change_set_applied")).toBe("Applied");
    expect(timelineKindLabel("change_set_rejected")).toBe("Rejected");
    expect(timelineKindLabel("change_set_denied")).toBe("Denied");
    expect(timelineKindLabel("change_set_reverted")).toBe("Reverted");
    expect(timelineKindLabel("run_failed")).toBe("Run failed");
    expect(timelineKindLabel("run_stopped")).toBe("Run stopped");
    expect(timelineKindLabel("decision_changed")).toBe("Decision changed");
    expect(timelineKindLabel("stale")).toBe("Stale");
    expect(timelineKindLabel("phase_completed")).toBe("Phase completed");
  });

  it("humanizes an unrecognized kind rather than dropping it", () => {
    expect(timelineKindLabel("future_kind_here")).toBe("Future Kind Here");
  });

  it("falls back to Update for an empty kind", () => {
    expect(timelineKindLabel("")).toBe("Update");
  });
});

describe("relativeTime", () => {
  const now = new Date("2026-09-18T12:10:00Z");

  it("says just now for under a minute", () => {
    expect(relativeTime("2026-09-18T12:09:30Z", now)).toBe("just now");
  });

  it("shows minutes ago", () => {
    expect(relativeTime("2026-09-18T12:05:00Z", now)).toBe("5m ago");
  });

  it("shows hours ago", () => {
    expect(relativeTime("2026-09-18T09:10:00Z", now)).toBe("3h ago");
  });

  it("shows days ago", () => {
    expect(relativeTime("2026-09-15T12:10:00Z", now)).toBe("3d ago");
  });

  it("falls back to a plain date at a week or more", () => {
    expect(relativeTime("2026-09-01T12:10:00Z", now)).toBe("2026-09-01");
  });

  it("treats a future timestamp (clock skew) as just now, not negative minutes", () => {
    expect(relativeTime("2026-09-18T12:20:00Z", now)).toBe("just now");
  });
});

describe("describeTimelineEntry", () => {
  it("combines badge and title with no detail", () => {
    expect(describeTimelineEntry(entry())).toBe("Applied: CHG-1 applied");
  });

  it("appends detail when present", () => {
    expect(describeTimelineEntry(entry({ detail: "3 files changed" }))).toBe(
      "Applied: CHG-1 applied — 3 files changed",
    );
  });
});

// localDayKey mirrors dayKey's own local-time bucketing, so this test's
// expectations are correct under whatever time zone it actually runs in
// (never hard-coded to UTC, which is the bug being pinned).
function localDayKey(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

describe("groupByDay", () => {
  it("buckets entries by calendar day in local time, preserving newest-first order", () => {
    // Constructed in local time (not UTC "Z" strings): dayKey must bucket by
    // the viewer's own calendar day, so the test's own expectation is
    // derived the same way rather than assuming UTC (review Low fix).
    const day1Late = new Date(2026, 8, 18, 12, 0, 0);
    const day1Early = new Date(2026, 8, 18, 9, 0, 0);
    const day0 = new Date(2026, 8, 17, 12, 0, 0);
    const entries = [
      entry({ at: day1Late.toISOString(), title: "a" }),
      entry({ at: day1Early.toISOString(), title: "b" }),
      entry({ at: day0.toISOString(), title: "c" }),
    ];
    const groups = groupByDay(entries);
    expect(groups.map((g) => g.day)).toEqual([localDayKey(day1Late), localDayKey(day0)]);
    expect(groups[0].entries.map((e) => e.title)).toEqual(["a", "b"]);
    expect(groups[1].entries.map((e) => e.title)).toEqual(["c"]);
  });

  it("returns an empty list for no entries", () => {
    expect(groupByDay([])).toEqual([]);
  });
});

describe("timelineEntryHref (review Low fix: the ref was never linked)", () => {
  it("links a change_set ref to the Build page", () => {
    expect(timelineEntryHref("ini-1", { type: "change_set", id: "CHG-1" })).toBe("#/i/ini-1/build");
  });

  it("links a decision ref to that decision's discussion", () => {
    expect(timelineEntryHref("ini-1", { type: "decision", id: "dec-1" })).toBe("#/i/ini-1/decision/dec-1");
  });

  it("is null for a ref type with no page of its own", () => {
    expect(timelineEntryHref("ini-1", { type: "operation", id: "op-1" })).toBeNull();
  });

  it("is null with no ref at all", () => {
    expect(timelineEntryHref("ini-1", undefined)).toBeNull();
  });
});
