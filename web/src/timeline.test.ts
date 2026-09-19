import { describe, expect, it } from "vitest";
import {
  describeTimelineEntry,
  groupByDay,
  relativeTime,
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

describe("groupByDay", () => {
  it("buckets entries by calendar day, preserving newest-first order", () => {
    const entries = [
      entry({ at: "2026-09-18T12:00:00Z", title: "a" }),
      entry({ at: "2026-09-18T09:00:00Z", title: "b" }),
      entry({ at: "2026-09-17T12:00:00Z", title: "c" }),
    ];
    const groups = groupByDay(entries);
    expect(groups.map((g) => g.day)).toEqual(["2026-09-18", "2026-09-17"]);
    expect(groups[0].entries.map((e) => e.title)).toEqual(["a", "b"]);
    expect(groups[1].entries.map((e) => e.title)).toEqual(["c"]);
  });

  it("returns an empty list for no entries", () => {
    expect(groupByDay([])).toEqual([]);
  });
});
