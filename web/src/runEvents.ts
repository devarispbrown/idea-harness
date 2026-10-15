// runEvents: the pure side of the run view (RunPanel.tsx). Everything here is
// a function of the events received so far, so it is unit-tested without a
// browser, a daemon or a stream: merging pages and stream messages (which
// overlap on reconnect), and deriving what the panel shows -- the current
// action, grouped tool calls, the file list, permissions, the meter, errors
// and steering notes.

export type RunEventKind =
  | "action"
  | "tool_call"
  | "tool_result"
  | "file_change"
  | "permission"
  | "message"
  | "thought"
  | "cost"
  | "status"
  | "error";

// RunEvent mirrors domain.RunEvent (internal/domain/runevent.go).
export interface RunEvent {
  seq: number;
  run_id: string;
  operation_id?: string;
  initiative_id?: string;
  task_id?: string;
  executor?: string;
  at: string;
  kind: RunEventKind;
  source?: "agent" | "harness" | "user";
  title: string;
  detail?: string;
  path?: string;
  tool_call_id?: string;
  tool_kind?: string;
  status?: string;
  added?: number;
  removed?: number;
  input_tokens?: number;
  output_tokens?: number;
  cost_usd?: number;
  change_set_id?: string;
  truncated?: boolean;
}

// RunEventPage mirrors GET .../runs/{runId}/events.
export interface RunEventPage {
  run_id: string;
  run_status: string;
  events: RunEvent[];
  first_seq: number;
  last_seq: number;
  more: boolean;
}

export interface RunEventState {
  events: RunEvent[];
  lastSeq: number;
}

export const emptyRunEventState: RunEventState = { events: [], lastSeq: 0 };

// maxKeptEvents bounds what the panel holds in memory; the server keeps more
// (and the report has them all).
export const maxKeptEvents = 3000;

// mergeEvents folds newly received events into the state: sorted by seq,
// duplicates (a page and a stream message overlapping on reconnect) dropped,
// anything at or below the cursor ignored. Returns the same object when
// nothing changed, so a React state setter skips the render.
export function mergeEvents(state: RunEventState, incoming: RunEvent[]): RunEventState {
  const fresh = incoming
    .filter((e) => e && typeof e.seq === "number" && e.seq > state.lastSeq)
    .sort((a, b) => a.seq - b.seq);
  if (fresh.length === 0) return state;
  const out: RunEvent[] = [];
  let last = state.lastSeq;
  for (const e of fresh) {
    if (e.seq <= last) continue; // a duplicate within the batch
    out.push(e);
    last = e.seq;
  }
  let events = state.events.concat(out);
  if (events.length > maxKeptEvents) events = events.slice(events.length - maxKeptEvents);
  return { events, lastSeq: last };
}

// ---------- the current action ----------

const verbs: Record<string, string> = {
  read: "reading",
  edit: "editing",
  delete: "deleting",
  move: "moving",
  search: "searching",
  execute: "running",
  fetch: "fetching",
  think: "thinking",
  file_editor: "editing",
  str_replace_editor: "editing",
  terminal: "running",
  execute_bash: "running",
};

function toolPhrase(e: RunEvent): string {
  const verb = verbs[e.tool_kind ?? ""];
  const object = e.path || e.detail || "";
  if (verb && object) return `${verb} ${object}`;
  if (verb === "running" && !object) return e.title || "running a command";
  return e.title || verb || "working";
}

function isOpen(status?: string): boolean {
  return !status || status === "pending" || status === "in_progress";
}

// currentAction is the chip text: the newest tool call still open, else
// "Thinking" when the latest agent step is a thought (or a live run has said
// nothing yet), else the latest status title. "" when the run is over.
export function currentAction(events: RunEvent[], live: boolean): string {
  if (!live) return "";
  const calls = groupToolCalls(events);
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    if (e.kind === "thought") return "Thinking";
    if (e.kind === "tool_call" || e.kind === "tool_result") {
      const g = calls.find((c) => c.id === (e.tool_call_id || `seq-${e.seq}`));
      if (g && isOpen(g.status)) return toolPhrase(g.call);
      if (g) return "Thinking"; // the call just finished; the agent is deciding
    }
    if (e.kind === "file_change" && e.source !== "harness") return `editing ${e.path ?? ""}`.trim();
    if (e.kind === "action" && e.source === "agent") return e.title;
    if (e.kind === "status") return e.title || "Thinking";
    if (e.kind === "message" && e.source === "agent") return "Thinking";
  }
  return "Thinking";
}

// ---------- tool calls ----------

export interface ToolCallGroup {
  id: string;
  call: RunEvent; // the tool_call (or the first result, if the call was dropped)
  title: string;
  status: string; // latest: pending | in_progress | completed | failed
  results: RunEvent[];
}

// groupToolCalls collects tool_call/tool_result events by tool_call_id, in
// the order the calls started. A call with no id is its own group.
export function groupToolCalls(events: RunEvent[]): ToolCallGroup[] {
  const byID = new Map<string, ToolCallGroup>();
  const order: string[] = [];
  for (const e of events) {
    if (e.kind !== "tool_call" && e.kind !== "tool_result") continue;
    const id = e.tool_call_id || `seq-${e.seq}`;
    let g = byID.get(id);
    if (!g) {
      g = { id, call: e, title: e.title, status: e.status || "pending", results: [] };
      byID.set(id, g);
      order.push(id);
    }
    if (e.kind === "tool_call") {
      g.call = e;
      if (e.title) g.title = e.title;
    } else {
      g.results.push(e);
    }
    if (e.status) g.status = e.status;
  }
  return order.map((id) => byID.get(id)!);
}

// actionSummary is "7/12 actions": finished (completed or failed) of all.
export function actionSummary(groups: ToolCallGroup[]): { done: number; total: number; failed: number; label: string } {
  const done = groups.filter((g) => !isOpen(g.status)).length;
  const failed = groups.filter((g) => g.status === "failed").length;
  const total = groups.length;
  return { done, total, failed, label: `${done}/${total} action${total === 1 ? "" : "s"}` };
}

// ---------- files ----------

export interface FileEntry {
  path: string;
  added: number;
  removed: number;
  change?: string; // add | modify | delete, once the final diff has it
  final: boolean; // counts are from the orchestrator's own diff
}

const finalChanges = new Set(["add", "modify", "delete"]);

// isFinalFileEvent reports whether e is the orchestrator's authoritative diff
// entry (source harness, with a change type), rather than a live report.
export function isFinalFileEvent(e: RunEvent): boolean {
  return e.kind === "file_change" && e.source === "harness" && finalChanges.has(e.status ?? "");
}

// fileList merges file_change events by path. A live event's counts
// accumulate (each write adds its own +/-); the final diff replaces them.
export function fileList(events: RunEvent[]): FileEntry[] {
  const byPath = new Map<string, FileEntry>();
  const order: string[] = [];
  for (const e of events) {
    if (e.kind !== "file_change" || !e.path) continue;
    let f = byPath.get(e.path);
    if (!f) {
      f = { path: e.path, added: 0, removed: 0, final: false };
      byPath.set(e.path, f);
      order.push(e.path);
    }
    if (isFinalFileEvent(e)) {
      f.added = e.added ?? 0;
      f.removed = e.removed ?? 0;
      f.change = e.status;
      f.final = true;
    } else if (!f.final) {
      f.added += e.added ?? 0;
      f.removed += e.removed ?? 0;
    }
  }
  return order.map((p) => byPath.get(p)!);
}

// ---------- permissions, errors, notes, meter ----------

export interface PermissionEntry {
  title: string;
  decision: string;
  toolKind?: string;
  at: string;
}

export function permissions(events: RunEvent[]): PermissionEntry[] {
  return events
    .filter((e) => e.kind === "permission")
    .map((e) => ({ title: e.title, decision: e.status ?? "", toolKind: e.tool_kind, at: e.at }));
}

export function errors(events: RunEvent[]): RunEvent[] {
  return events.filter((e) => e.kind === "error");
}

export function steeringNotes(events: RunEvent[]): RunEvent[] {
  return events.filter((e) => e.kind === "message" && e.source === "user");
}

export interface Meter {
  inputTokens: number;
  outputTokens: number;
  costUSD: number;
}

export function meter(events: RunEvent[]): Meter {
  const m: Meter = { inputTokens: 0, outputTokens: 0, costUSD: 0 };
  for (const e of events) {
    if (e.kind !== "cost") continue;
    m.inputTokens += e.input_tokens ?? 0;
    m.outputTokens += e.output_tokens ?? 0;
    m.costUSD += e.cost_usd ?? 0;
  }
  return m;
}

// changeSetOf is the change set the run produced (its "finished" event).
export function changeSetOf(events: RunEvent[]): string {
  for (let i = events.length - 1; i >= 0; i--) {
    if (events[i].change_set_id) return events[i].change_set_id!;
  }
  return "";
}

export function fmtElapsed(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${sec}s`;
  return `${sec}s`;
}

export function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(n);
}

// ---------- resume and reconnect ----------

// backoffDelay is the wait before reconnect attempt n (0-based): 1s, 2s, 4s,
// ... capped at 30s, with up to 20% jitter so many tabs reconnecting after a
// daemon restart do not arrive in lockstep. rand is injectable for tests.
export function backoffDelay(attempt: number, rand: () => number = Math.random): number {
  const base = Math.min(30_000, 1000 * 2 ** Math.max(0, Math.min(attempt, 10)));
  return Math.round(base * (1 + 0.2 * rand()));
}

// streamURL builds a stream URL that resumes after the cursor. EventSource
// sends Last-Event-ID itself only on its OWN reconnects; a stream this code
// recreates (after a backoff, or a tab coming back into view) carries the
// cursor as ?after= instead. The token rides as ?token= because EventSource
// cannot set a header (the daemon accepts it on the two stream paths only).
export function streamURL(path: string, cursor: string, token: string): string {
  const q = new URLSearchParams();
  if (token) q.set("token", token);
  if (cursor && cursor !== "0") q.set("after", cursor);
  const qs = q.toString();
  return "/api/v1" + path + (qs ? (path.includes("?") ? "&" : "?") + qs : "");
}

// shouldFallBack decides when to give up on SSE and poll instead: the
// browser has no EventSource, or the stream failed `limit` times in a row
// without ever opening (a proxy that strips text/event-stream, say).
export function shouldFallBack(hasEventSource: boolean, failuresBeforeOpen: number, limit = 3): boolean {
  return !hasEventSource || failuresBeforeOpen >= limit;
}

// projectCursor parses the project stream's "<event seq>.<run event id>" id.
export function projectCursor(id: string): { seq: number; run: number } | null {
  const m = /^(\d+)(?:\.(\d+))?$/.exec(id.trim());
  if (!m) return null;
  return { seq: Number(m[1]), run: m[2] ? Number(m[2]) : 0 };
}
