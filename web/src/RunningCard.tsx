import { useCallback, useEffect, useRef, useState } from "react";
import { get, post } from "./api";
import { type NextTask, resolveRunningTask, runningTitle } from "./buildLogic";

interface Operation {
  id: string;
  type: string;
  status: string;
  created_at: string;
  finished_at?: string;
  // Set on the operation the moment it starts, on a daemon that stamps it
  // (optional so this still works against one that doesn't yet). When
  // present it's authoritative for which task THIS run is working on, even
  // after guidance's "next" pointer has moved on to a different task.
  task_id?: string;
  task_human_id?: string;
  task_title?: string;
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
      // The task this execute is working on. Prefer the OPERATION's own task
      // fields when it carries them -- correct even once guidance's "next"
      // pointer has already moved on to a different task -- and only fall
      // back to a guidance fetch (H4: next_task lives on GET .../guidance,
      // not on the initiative resource, which is what this used to read and
      // always came back empty) when the operation doesn't have them yet.
      const fromOp = resolveRunningTask(running, undefined);
      if (fromOp) {
        setTask(fromOp);
      } else {
        // Best-effort: a transient error just leaves the previous title in
        // place rather than blanking it.
        try {
          const guide = await get<{ next_task?: NextTask | null }>(
            `/projects/${projectId}/initiatives/${initiativeId}/guidance`,
          );
          if (gen === genRef.current) setTask(guide?.next_task ?? null);
        } catch {
          /* keep the previous task label */
        }
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
    // No "Run again" button here: this used to duplicate the phase page's
    // own primary run action (BuildPage's "Run TASK-2: ..." button, driven
    // by runButtonState) -- once nothing is running, that button already
    // shows itself, so both offering the identical POST .../execute at once
    // read as two competing controls for one action. This card stays
    // (design rule 8: running/having-just-stopped is never silent) but is
    // purely informational for the stopped state; the page's own button is
    // the one primary way to start again.
    return (
      <section className="running-card running-card-stopped">
        <div className="running-row">
          <div className="running-body">
            <div className="running-title">Stopped.</div>
          </div>
        </div>
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
