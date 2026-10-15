import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { apiBlob, get, post, resource } from "./api";
import {
  actionSummary,
  currentAction,
  emptyRunEventState,
  errors,
  fileList,
  fmtElapsed,
  fmtTokens,
  groupToolCalls,
  mergeEvents,
  meter,
  permissions,
  steeringNotes,
  type RunEvent,
  type RunEventPage,
  type RunEventState,
} from "./runEvents";
import { useEventStream, useProjectChanges } from "./useEventStream";
import { announce, RUN_RANK, runStatusAnnouncement, runTopic } from "./announce";
import { isSubmitShortcut, modLabel } from "./shortcuts";
import RemoteRuns from "./RemoteRuns";

// CurrentRun mirrors GET .../initiatives/{id}/current-run.
interface CurrentRun {
  run_id: string;
  live: boolean;
  status?: string;
  executor?: string;
  steerable: boolean;
  ui_url?: string;
  pending_notes: string[];
  started_at?: string;
}

interface SteerResult {
  run_id: string;
  executor?: string;
  delivery: "delivered" | "queued" | "next_run";
  message: string;
}

// RunPanel is the live view of an initiative's coding-agent run (Build page,
// inside the running card): what the agent is doing now, its tool calls, the
// files it is changing, the permissions answered on the operator's behalf,
// tokens/cost/elapsed, errors, and a box to send it a note. It works after a
// reload or a dropped connection mid-run: it reads the run's stored events
// (paging, from the start) and then streams from the last one it has.
//
// Every string from the agent is rendered as text, never as HTML.
export default function RunPanel({
  projectId,
  initiativeId,
  active,
  idleOnly = false,
}: {
  projectId: string;
  initiativeId: string;
  // active: an execute operation is in flight, so a run is (or soon will be)
  // live; the panel re-reads current-run until it has one.
  active: boolean;
  // idleOnly: the Build page's "Last agent run" -- shown, collapsed, only
  // while no run is live (the running card shows a live one), so a finished
  // run's files, events and report stay one click away.
  idleOnly?: boolean;
}) {
  const [run, setRun] = useState<CurrentRun | null>(null);
  const [state, setState] = useState<RunEventState>(emptyRunEventState);
  const [ended, setEnded] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const stateRef = useRef(state);
  stateRef.current = state;

  // Which run to show: re-read while the execute is active and the run is
  // not live yet (the planner runs first; the run exists only once it has a
  // brief), and once more when the operation ends.
  const loadRun = useCallback(async () => {
    try {
      const r = await get<CurrentRun>(resource(projectId, initiativeId, "current-run"));
      setRun((prev) => {
        if (prev && r && prev.run_id === r.run_id && prev.live === r.live && prev.steerable === r.steerable &&
          prev.ui_url === r.ui_url && (prev.pending_notes ?? []).join("\n") === (r.pending_notes ?? []).join("\n")) {
          return prev;
        }
        return r;
      });
    } catch {
      /* keep what we have */
    }
  }, [projectId, initiativeId]);

  // The idle "Last agent run" follows the project's stream, so a run that
  // starts or finishes elsewhere replaces it without a reload.
  useProjectChanges(idleOnly ? projectId : "", loadRun);

  useEffect(() => {
    loadRun();
    if (!active) return;
    const id = setInterval(loadRun, 3000);
    return () => clearInterval(id);
  }, [loadRun, active]);

  const runId = run?.run_id ?? "";
  const runPath = `/projects/${encodeURIComponent(projectId)}/runs/${encodeURIComponent(runId)}`;

  // A different run starts from nothing.
  useEffect(() => {
    stateRef.current = emptyRunEventState; // catchUp below reads the ref
    setState(emptyRunEventState);
    setEnded(false);
  }, [runId]);

  // Late joiner / poll fallback: page through what is stored after the
  // cursor.
  const catchUp = useCallback(async () => {
    if (!runId) return;
    for (let i = 0; i < 20; i++) {
      const page = await get<RunEventPage>(`${runPath}/events?after=${stateRef.current.lastSeq}`);
      if (page.events.length > 0) {
        const next = mergeEvents(stateRef.current, page.events);
        stateRef.current = next;
        setState(next);
      }
      if (!page.more) {
        if (page.run_status === "succeeded" || page.run_status === "failed") setEnded(true);
        return;
      }
    }
  }, [runId, runPath]);

  const [caughtUp, setCaughtUp] = useState("");
  useEffect(() => {
    if (!runId) return;
    let cancelled = false;
    catchUp()
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setCaughtUp(runId);
      });
    return () => {
      cancelled = true;
    };
  }, [runId, catchUp]);

  const live = !!run?.live && !ended;
  const mode = useEventStream({
    // Stream only once the stored events are in (so the stream resumes
    // after them rather than replaying them) and while the run is live.
    key: runId && caughtUp === runId && live ? "run:" + runId : "",
    path: `${runPath}/events/stream`,
    eventName: "run_event",
    getCursor: () => String(stateRef.current.lastSeq),
    onMessage: (data) => {
      try {
        const ev = JSON.parse(data) as RunEvent;
        const next = mergeEvents(stateRef.current, [ev]);
        if (next !== stateRef.current) {
          stateRef.current = next;
          setState(next);
        }
      } catch {
        /* a malformed message is skipped */
      }
    },
    onEnd: () => {
      setEnded(true);
      loadRun();
    },
    poll: catchUp,
    pollMs: 2000,
  });

  useEffect(() => {
    if (!live) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [live]);

  // A run going live is said once in the app's polite live region, not per
  // event: the action chip below changes every few seconds and is
  // deliberately not a live region. Only the live panel speaks (the idle
  // "Last agent run" is history); the running card says when it is over.
  // The stopped card's panel (active false) shows a run that is over even
  // while its stream has not said so yet: it is not news, so it is silent.
  // Its start shares the run topic with the card's "Executing the plan …",
  // so a run that starts and ends within one poll is said once (announce.ts).
  const lastAnnounced = useRef<{ runId: string; live: boolean } | null>(null);
  useEffect(() => {
    if (idleOnly || !active || !runId) return;
    const msg = runStatusAnnouncement(lastAnnounced.current, { runId, live });
    if (msg) announce(msg, { topic: runTopic(initiativeId), rank: RUN_RANK.runStarted });
    lastAnnounced.current = { runId, live };
  }, [idleOnly, active, runId, live, initiativeId]);

  const events = state.events;
  const groups = useMemo(() => groupToolCalls(events, !live), [events, live]);
  const summary = actionSummary(groups);
  const files = useMemo(() => fileList(events), [events]);
  const perms = useMemo(() => permissions(events), [events]);
  const errs = useMemo(() => errors(events), [events]);
  const notes = useMemo(() => steeringNotes(events), [events]);
  const m = useMemo(() => meter(events), [events]);
  const chip = currentAction(events, live);

  if (!run || !runId) return null;
  if (idleOnly && run.live) return null;

  const started = run.started_at ? Date.parse(run.started_at) : NaN;
  const lastAt = events.length > 0 ? Date.parse(events[events.length - 1].at) : NaN;
  const elapsed = Number.isFinite(started) ? (live ? now : Number.isFinite(lastAt) ? lastAt : now) - started : NaN;

  const panel = (
    <section className="run-panel" aria-label="Agent run">
      <div className="run-panel-head">
        {live ? (
          <span className="run-chip">
            <span className="run-chip-dot" aria-hidden="true" />
            {chip || "Thinking"}
          </span>
        ) : (
          <span className="run-chip run-chip-done">Run {run.status ?? "finished"}</span>
        )}
        <span className="run-meter" aria-label="Run meter">
          {Number.isFinite(elapsed) && <span>{fmtElapsed(elapsed)}</span>}
          {(m.inputTokens > 0 || m.outputTokens > 0) && (
            <span>
              {fmtTokens(m.inputTokens)} in · {fmtTokens(m.outputTokens)} out
            </span>
          )}
          {m.costUSD > 0 && <span>${m.costUSD.toFixed(m.costUSD < 1 ? 4 : 2)}</span>}
          {run.executor && <span className="muted">{run.executor}</span>}
          {live && mode === "polling" && <span className="muted" title="Live updates are unavailable; refreshing every 2s">polling</span>}
        </span>
        <span className="run-panel-links">
          {run.ui_url && (
            <a href={run.ui_url} target="_blank" rel="noopener noreferrer">
              Open in OpenHands
            </a>
          )}
          <ReportButtons runPath={runPath} runId={runId} />
        </span>
      </div>

      {run.executor === "openhands" && <RemoteRuns projectId={projectId} initiativeId={initiativeId} />}

      {errs.length > 0 && (
        <div className="run-errors" role="alert">
          {errs.slice(-3).map((e) => (
            <div key={e.seq} className="run-error">
              <strong>{e.title}</strong>
              {e.detail && e.detail !== e.title && <pre className="run-error-detail">{e.detail}</pre>}
            </div>
          ))}
        </div>
      )}

      <div className="run-panel-grid">
        <details className="run-actions">
          <summary>
            {summary.total === 0 ? "No tool calls yet" : summary.label}
            {summary.failed > 0 && <span className="run-failed-count"> · {summary.failed} failed</span>}
            {summary.cancelled > 0 && <span className="muted"> · {summary.cancelled} cancelled</span>}
          </summary>
          <ol className="run-action-list">
            {groups.slice(-200).map((g) => (
              <li key={g.id} className={"run-action run-action-" + (g.status || "pending")}>
                <span className="run-action-status" role="img" aria-label={(g.status || "pending").replace(/_/g, " ")}>
                  {g.status === "completed" ? "✓" : g.status === "failed" ? "✗" : g.status === "cancelled" ? "⊘" : "…"}
                </span>{" "}
                {g.title}
                {g.call.detail && g.call.detail !== g.title && <code className="run-action-detail">{g.call.detail}</code>}
              </li>
            ))}
          </ol>
        </details>

        <div className="run-files">
          <div className="run-section-title">Files {files.length > 0 && `(${files.length})`}</div>
          {files.length === 0 ? (
            <p className="muted">No files changed yet.</p>
          ) : (
            <ul className="run-file-list">
              {files.slice(0, 100).map((f) => (
                <li key={f.path}>
                  <code>{f.path}</code>
                  {(f.added > 0 || f.removed > 0) && (
                    <span className="run-file-counts">
                      <span className="run-added">+{f.added}</span> <span className="run-removed">−{f.removed}</span>
                    </span>
                  )}
                  {f.change === "delete" && <span className="muted"> deleted</span>}
                </li>
              ))}
              {files.length > 100 && <li className="muted">…and {files.length - 100} more</li>}
            </ul>
          )}
        </div>
      </div>

      {perms.length > 0 && (
        <details className="run-permissions">
          <summary>
            {perms.length} permission{perms.length === 1 ? "" : "s"} answered for you
          </summary>
          <ul>
            {perms.map((p, i) => (
              <li key={i}>
                <span className={p.decision.startsWith("allow") ? "run-allow" : "run-deny"}>{p.decision.replace(/_/g, " ")}</span> {p.title}
              </li>
            ))}
          </ul>
        </details>
      )}

      {(run.pending_notes ?? []).length > 0 && (
        <p className="run-pending muted">
          {run.pending_notes.length} note{run.pending_notes.length === 1 ? "" : "s"} will be added to this task's next run.
        </p>
      )}

      {notes.length > 0 && (
        <ul className="run-notes">
          {notes.map((n) => (
            <li key={n.seq}>
              <span className="muted">{n.title} ({(n.status ?? "").replace(/_/g, " ")}):</span> {n.detail}
            </li>
          ))}
        </ul>
      )}

      {live && (
        <SteerBox
          runPath={runPath}
          executor={run.executor ?? ""}
          steerable={run.steerable}
          onSent={() => {
            loadRun();
          }}
        />
      )}
    </section>
  );
  if (!idleOnly) return panel;
  return (
    <details className="run-last">
      <summary>
        Last agent run · {(run.status ?? "finished").replace(/_/g, " ")}
        {run.executor ? ` · ${run.executor}` : ""}
      </summary>
      {panel}
    </details>
  );
}

function SteerBox({
  runPath,
  executor,
  steerable,
  onSent,
}: {
  runPath: string;
  executor: string;
  steerable: boolean;
  onSent: () => void;
}) {
  const [note, setNote] = useState("");
  const [interrupt, setInterrupt] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null);

  const send = async () => {
    if (!note.trim()) return;
    setBusy(true);
    setResult(null);
    try {
      const r = await post<SteerResult>(`${runPath}/steer`, { note, interrupt: steerable && interrupt });
      setResult({ ok: true, text: r.message });
      setNote("");
      onSent();
    } catch (e) {
      setResult({ ok: false, text: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="run-steer">
      <label className="run-section-title" htmlFor="run-steer-note">
        Send the agent a note
      </label>
      {!steerable && (
        <p className="muted run-steer-hint">
          Steering not supported by {executor || "this executor"}; the note will apply to the next run.
        </p>
      )}
      <textarea
        id="run-steer-note"
        rows={2}
        value={note}
        maxLength={8000}
        placeholder={steerable ? "e.g. Use the existing date helper instead of adding a dependency" : "Added to this task's next run"}
        aria-describedby="run-steer-shortcut"
        onChange={(e) => setNote(e.target.value)}
        onKeyDown={(e) => {
          if (isSubmitShortcut(e.nativeEvent)) {
            e.preventDefault();
            if (!busy) send();
          }
        }}
      />
      <span id="run-steer-shortcut" className="note-field-hint">
        <kbd>{modLabel()}</kbd> <kbd>Enter</kbd> to send
      </span>
      <div className="run-steer-row">
        {steerable && (
          <label className="run-steer-interrupt">
            <input type="checkbox" checked={interrupt} onChange={(e) => setInterrupt(e.target.checked)} /> Interrupt the current step
          </label>
        )}
        <button onClick={send} disabled={busy || !note.trim()} aria-label="Send note to the agent">
          {busy ? "Sending…" : "Send note"}
        </button>
      </div>
      {result && (
        <p className={result.ok ? "muted run-steer-result" : "run-steer-error"} role="status">
          {result.text}
        </p>
      )}
    </div>
  );
}

function ReportButtons({ runPath, runId }: { runPath: string; runId: string }) {
  const [error, setError] = useState("");
  const download = async (format: "json" | "md") => {
    setError("");
    try {
      const blob = await apiBlob(`${runPath}/report?format=${format}`);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `run-${runId}.${format}`;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };
  return (
    <span className="run-report">
      <button className="ghost" onClick={() => download("json")} aria-label="Download the run report as JSON">
        Report JSON
      </button>
      <button className="ghost" onClick={() => download("md")} aria-label="Download the run report as Markdown">
        Report MD
      </button>
      {error && <span className="run-steer-error">{error}</span>}
    </span>
  );
}
