import { useCallback, useEffect, useRef, useState } from "react";
import { get, post } from "./api";
import { errMessage, type NextTask, runningTitle } from "./buildLogic";

interface Operation {
  id: string;
  type: string;
  status: string;
  created_at: string;
  finished_at?: string;
}

const OP_LABEL: Record<string, string> = {
  run_initiative: "Running the workflow",
  advance_initiative: "Advancing the phase",
  execute_initiative: "Executing the plan",
  apply_change_set: "Applying change set",
  revert_change_set: "Reverting change set",
  revise_initiative: "Revising",
  revise_plan: "Revising the plan",
  approve_plan: "Approving the plan",
  suggest_answers: "Suggesting answers",
};

function fmtElapsed(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) return `${h}h ${m}m ${sec}s`;
  if (m > 0) return `${m}m ${sec}s`;
  return `${sec}s`;
}

// RunningCard surfaces the initiative's current running operation with a live
// elapsed timer and a Stop action (design rule 8: running never silent). It
// renders nothing when no operation is running/pending for this initiative
// AND nothing was just stopped by the person here (B3: after Stop, it shows
// a "Stopped. Run again?" state instead of vanishing -- a card that just
// disappears reads as "did that even do anything?").
export default function RunningCard({
  projectId,
  initiativeId,
}: {
  projectId: string;
  initiativeId: string;
}) {
  const [op, setOp] = useState<Operation | null>(null);
  const [stopped, setStopped] = useState<Operation | null>(null);
  const [task, setTask] = useState<NextTask | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [stopBusy, setStopBusy] = useState(false);
  const [runAgainBusy, setRunAgainBusy] = useState(false);
  const [error, setError] = useState("");
  // A generation counter guards against a stale poll response from a previous
  // initiative resolving after the props change.
  const genRef = useRef(0);

  const load = useCallback(async () => {
    const gen = genRef.current;
    let ops: Operation[];
    try {
      ops = await get<Operation[]>(
        `/projects/${projectId}/initiatives/${initiativeId}/operations`,
      );
    } catch {
      return; // transient error: keep the current op until the next poll
    }
    if (gen !== genRef.current) return; // stale response for a prior initiative
    const running = (ops ?? []).find(
      (o) => o.status === "running" || o.status === "pending",
    );
    setOp(running ?? null);
    if (running) {
      setNow(Date.now());
      setStopped(null); // a fresh run (Run again, or a new task) supersedes it
    }
    if (running?.type === "execute_initiative") {
      // The task this execute is working on. Best-effort: a transient error
      // just leaves the previous title in place rather than blanking it.
      try {
        const ini = await get<{ next_task?: NextTask | null }>(
          `/projects/${projectId}/initiatives/${initiativeId}`,
        );
        if (gen === genRef.current) setTask(ini?.next_task ?? null);
      } catch {
        /* keep the previous task label */
      }
    } else {
      setTask(null);
    }
  }, [projectId, initiativeId]);

  useEffect(() => {
    genRef.current++;
    setStopped(null);
    load();
    const poll = setInterval(load, 2500);
    return () => clearInterval(poll);
  }, [load]);

  // Tick the elapsed timer only while an operation is running (and reset its
  // baseline when a new op appears), so a hidden card never re-renders.
  useEffect(() => {
    if (!op) return;
    const tick = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(tick);
  }, [op]);

  if (!op) {
    if (!stopped) return null;
    return (
      <section className="running-card running-card-stopped">
        <div className="running-row">
          <div className="running-body">
            <div className="running-title">Stopped. Run again?</div>
          </div>
          {stopped.type === "execute_initiative" && (
            <button
              className="running-stop"
              disabled={runAgainBusy}
              onClick={async () => {
                setRunAgainBusy(true);
                setError("");
                try {
                  await post(`/projects/${projectId}/initiatives/${initiativeId}/execute`, {});
                  // Left stopped=true here on purpose: load() below clears it
                  // once the poll actually observes the new op running, so
                  // the card never blanks between "started" and "observed".
                } catch (e) {
                  setError(errMessage(e));
                } finally {
                  setRunAgainBusy(false);
                  await load();
                }
              }}
            >
              {runAgainBusy ? "Starting…" : "Run again"}
            </button>
          )}
        </div>
        {error && <p className="error">{error}</p>}
      </section>
    );
  }

  const stop = async () => {
    setStopBusy(true);
    try {
      await post(`/operations/${op.id}/cancel`, {});
      setStopped(op);
    } catch {
      /* the poll reconciles on its own */
    } finally {
      setStopBusy(false);
      await load();
    }
  };

  return (
    <section className="running-card">
      <div className="running-row">
        <span className="pill-spinner" aria-hidden="true" />
        <div className="running-body">
          <div className="running-title">{runningTitle(op.type, OP_LABEL, task)}</div>
          <div className="running-sub" aria-hidden="true">
            Running for {fmtElapsed(now - Date.parse(op.created_at))} · you can leave this page
          </div>
        </div>
        <button className="running-stop" onClick={stop} disabled={stopBusy}>
          {stopBusy ? "Stopping…" : "Stop"}
        </button>
      </div>
    </section>
  );
}
