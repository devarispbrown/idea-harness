import { describe, expect, it } from "vitest";
import {
  actionSummary,
  backoffDelay,
  changeSetOf,
  currentAction,
  emptyRunEventState,
  errors,
  fileList,
  groupToolCalls,
  isFinalFileEvent,
  maxKeptEvents,
  mergeEvents,
  meter,
  permissions,
  projectCursor,
  shouldFallBack,
  steeringNotes,
  streamURL,
  type RunEvent,
} from "./runEvents";

let n = 0;
function ev(p: Partial<RunEvent>): RunEvent {
  n++;
  return { seq: n, run_id: "r1", at: "2026-10-14T00:00:00Z", kind: "action", title: "", ...p };
}

describe("mergeEvents", () => {
  it("appends in seq order and advances the cursor", () => {
    const s = mergeEvents(emptyRunEventState, [ev({ seq: 2 }), ev({ seq: 1 }), ev({ seq: 3 })]);
    expect(s.events.map((e) => e.seq)).toEqual([1, 2, 3]);
    expect(s.lastSeq).toBe(3);
  });

  it("drops what it has already seen (a page and a stream overlapping on reconnect)", () => {
    const a = mergeEvents(emptyRunEventState, [ev({ seq: 1 }), ev({ seq: 2 })]);
    const b = mergeEvents(a, [ev({ seq: 2 }), ev({ seq: 3 }), ev({ seq: 3 })]);
    expect(b.events.map((e) => e.seq)).toEqual([1, 2, 3]);
    expect(b.lastSeq).toBe(3);
  });

  it("returns the same state when nothing is new, so React skips the render", () => {
    const a = mergeEvents(emptyRunEventState, [ev({ seq: 5 })]);
    expect(mergeEvents(a, [ev({ seq: 4 }), ev({ seq: 5 })])).toBe(a);
    expect(mergeEvents(a, [])).toBe(a);
  });

  it("tolerates a gap: a capped run resumes from its first kept seq", () => {
    const s = mergeEvents(emptyRunEventState, [ev({ seq: 4001 }), ev({ seq: 4002 })]);
    expect(s.lastSeq).toBe(4002);
  });

  it("bounds what it keeps", () => {
    const many = Array.from({ length: maxKeptEvents + 10 }, (_, i) => ev({ seq: i + 1 }));
    const s = mergeEvents(emptyRunEventState, many);
    expect(s.events.length).toBe(maxKeptEvents);
    expect(s.events[0].seq).toBe(11);
    expect(s.lastSeq).toBe(maxKeptEvents + 10);
  });
});

describe("groupToolCalls / actionSummary", () => {
  it("groups calls with their results and counts finished ones", () => {
    const events = [
      ev({ kind: "tool_call", tool_call_id: "a", title: "Read a.ts", tool_kind: "read", status: "pending" }),
      ev({ kind: "tool_result", tool_call_id: "a", title: "Read a.ts", status: "completed" }),
      ev({ kind: "tool_call", tool_call_id: "b", title: "npm test", tool_kind: "execute", status: "in_progress" }),
      ev({ kind: "tool_result", tool_call_id: "b", title: "npm test exited 1", status: "failed" }),
      ev({ kind: "tool_call", tool_call_id: "c", title: "Edit x.ts", tool_kind: "edit", status: "pending" }),
      ev({ kind: "message", title: "hello" }),
    ];
    const g = groupToolCalls(events);
    expect(g.map((x) => [x.id, x.status, x.results.length])).toEqual([
      ["a", "completed", 1],
      ["b", "failed", 1],
      ["c", "pending", 0],
    ]);
    expect(actionSummary(g)).toEqual({ done: 2, total: 3, failed: 1, label: "2/3 actions" });
  });

  it("makes a call without an id its own group", () => {
    const g = groupToolCalls([ev({ kind: "tool_call", title: "x" }), ev({ kind: "tool_call", title: "y" })]);
    expect(g.length).toBe(2);
    expect(actionSummary(g).label).toBe("0/2 actions");
  });

  it("keeps the call's title when a result has none", () => {
    const g = groupToolCalls([
      ev({ kind: "tool_call", tool_call_id: "a", title: "Edit x.ts" }),
      ev({ kind: "tool_result", tool_call_id: "a", title: "", status: "completed" }),
    ]);
    expect(g[0].title).toBe("Edit x.ts");
  });
});

describe("currentAction", () => {
  it("names the newest open tool call", () => {
    const events = [
      ev({ kind: "tool_call", tool_call_id: "a", tool_kind: "edit", path: "src/x.ts", title: "Edit", status: "in_progress" }),
    ];
    expect(currentAction(events, true)).toBe("editing src/x.ts");
  });

  it("names a command", () => {
    const events = [ev({ kind: "tool_call", tool_call_id: "a", tool_kind: "execute", detail: "npm test", title: "Run tests" })];
    expect(currentAction(events, true)).toBe("running npm test");
  });

  it("says Thinking after a thought, after a finished call, and when nothing happened yet", () => {
    expect(currentAction([ev({ kind: "thought", title: "Thinking" })], true)).toBe("Thinking");
    expect(
      currentAction(
        [
          ev({ kind: "tool_call", tool_call_id: "a", tool_kind: "read", path: "a" }),
          ev({ kind: "tool_result", tool_call_id: "a", status: "completed" }),
        ],
        true,
      ),
    ).toBe("Thinking");
    expect(currentAction([], true)).toBe("Thinking");
  });

  it("falls back to a status title, and is empty once the run is over", () => {
    expect(currentAction([ev({ kind: "status", title: "Running checks" })], true)).toBe("Running checks");
    expect(currentAction([ev({ kind: "status", title: "Running checks" })], false)).toBe("");
  });
});

describe("fileList", () => {
  it("accumulates live counts and lets the final diff override them", () => {
    const events = [
      ev({ kind: "file_change", path: "a.ts", added: 3, removed: 1, source: "agent" }),
      ev({ kind: "file_change", path: "a.ts", added: 2, source: "agent" }),
      ev({ kind: "file_change", path: "b.ts", source: "harness", title: "Changed b.ts" }), // live scan, no counts
      ev({ kind: "file_change", path: "a.ts", added: 4, removed: 0, source: "harness", status: "modify" }),
      ev({ kind: "file_change", path: "a.ts", added: 9, source: "agent" }), // late live event: ignored
    ];
    const f = fileList(events);
    expect(f).toEqual([
      { path: "a.ts", added: 4, removed: 0, change: "modify", final: true },
      { path: "b.ts", added: 0, removed: 0, final: false },
    ]);
  });

  it("only treats harness events with a change type as final", () => {
    expect(isFinalFileEvent(ev({ kind: "file_change", source: "harness", status: "add" }))).toBe(true);
    expect(isFinalFileEvent(ev({ kind: "file_change", source: "agent", status: "add" }))).toBe(false);
    expect(isFinalFileEvent(ev({ kind: "file_change", source: "harness" }))).toBe(false);
  });
});

describe("permissions, errors, notes, meter, change set", () => {
  const events = [
    ev({ kind: "permission", title: "Write src/a.ts", status: "allow_once", tool_kind: "edit" }),
    ev({ kind: "error", title: "boom" }),
    ev({ kind: "message", source: "user", title: "Note from you", detail: "use pnpm" }),
    ev({ kind: "message", source: "agent", title: "hi" }),
    ev({ kind: "cost", input_tokens: 100, output_tokens: 20, cost_usd: 0.01 }),
    ev({ kind: "cost", input_tokens: 50, cost_usd: 0.02 }),
    ev({ kind: "status", status: "finished", change_set_id: "cs1", title: "CHG-1 is review required" }),
  ];
  it("lists permissions with their decisions", () => {
    expect(permissions(events)).toEqual([{ title: "Write src/a.ts", decision: "allow_once", toolKind: "edit", at: events[0].at }]);
  });
  it("lists errors and the human's notes", () => {
    expect(errors(events).map((e) => e.title)).toEqual(["boom"]);
    expect(steeringNotes(events).map((e) => e.detail)).toEqual(["use pnpm"]);
  });
  it("sums cost deltas", () => {
    const m = meter(events);
    expect(m.inputTokens).toBe(150);
    expect(m.outputTokens).toBe(20);
    expect(m.costUSD).toBeCloseTo(0.03);
  });
  it("finds the change set", () => {
    expect(changeSetOf(events)).toBe("cs1");
  });
});

describe("resume helpers", () => {
  it("backs off exponentially to a 30s cap with bounded jitter", () => {
    expect(backoffDelay(0, () => 0)).toBe(1000);
    expect(backoffDelay(1, () => 0)).toBe(2000);
    expect(backoffDelay(3, () => 0)).toBe(8000);
    expect(backoffDelay(10, () => 0)).toBe(30000);
    expect(backoffDelay(50, () => 0)).toBe(30000);
    expect(backoffDelay(0, () => 1)).toBe(1200);
  });

  it("builds a resuming stream URL with the token", () => {
    expect(streamURL("/projects/p/runs/r/events/stream", "12", "tok")).toBe(
      "/api/v1/projects/p/runs/r/events/stream?token=tok&after=12",
    );
    expect(streamURL("/projects/p/events", "0", "")).toBe("/api/v1/projects/p/events");
    expect(streamURL("/projects/p/events", "5.9", "t")).toBe("/api/v1/projects/p/events?token=t&after=5.9");
  });

  it("falls back to polling without EventSource or after repeated failures before any open", () => {
    expect(shouldFallBack(false, 0)).toBe(true);
    expect(shouldFallBack(true, 2)).toBe(false);
    expect(shouldFallBack(true, 3)).toBe(true);
  });

  it("parses the project stream's composite cursor", () => {
    expect(projectCursor("12.7")).toEqual({ seq: 12, run: 7 });
    expect(projectCursor("12")).toEqual({ seq: 12, run: 0 });
    expect(projectCursor("x")).toBeNull();
  });
});
