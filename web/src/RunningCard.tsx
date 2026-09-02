import { useCallback, useEffect, useRef, useState } from "react";
import { get, post } from "./api";

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
// renders nothing when no operation is running/pending for this initiative.
export default function RunningCard({
  projectId,
  initiativeId,
}: {
  projectId: string;
  initiativeId: string;
}) {
  const [op, setOp] = useState<Operation | null>(null);
  const [now, setNow] = useState(() => Date.now());
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
    if (running) setNow(Date.now());
  }, [projectId, initiativeId]);

  useEffect(() => {
    genRef.current++;
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

  if (!op) return null;

  const stop = async () => {
    try {
      await post(`/operations/${op.id}/cancel`, {});
    } catch {
      /* the poll reconciles on its own */
    }
    await load();
  };

  return (
    <section className="running-card">
      <div className="running-row">
        <span className="pill-spinner" aria-hidden="true" />
        <div className="running-body">
          <div className="running-title">{OP_LABEL[op.type] ?? op.type}</div>
          <div className="running-sub" aria-hidden="true">
            Running for {fmtElapsed(now - Date.parse(op.created_at))} · you can leave this page
          </div>
        </div>
        <button className="running-stop" onClick={stop}>
          Stop
        </button>
      </div>
    </section>
  );
}
