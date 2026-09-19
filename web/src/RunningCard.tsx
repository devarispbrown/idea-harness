import { useCallback, useEffect, useRef, useState } from "react";
import { get, post } from "./api";
import {
  isOperationInFlight,
  type NextTask,
  type PartialWork,
  resolveRunningTask,
  stageOrTitle,
  stoppedMessage,
} from "./buildLogic";
import { shouldStickToBottom } from "./agentOutput";

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
  // stage (task 8, C3 wire contract): the step running now, e.g. "Writing
  // the PRD", "Reviewing the plan, cycle 2", "Agent running TASK-2" --
  // optional so this still works against a daemon that hasn't shipped it.
  stage?: string;
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
  // C3: what a stopped run left behind, read from guidance once there's
  // nothing running -- used only by the post-Stop render below. Kept as a
  // ref alongside the state so `load` (a stable useCallback) can decide
  // whether it's worth fetching without re-creating itself every time Stop
  // is clicked.
  const [partialWork, setPartialWork] = useState<PartialWork | null>(null);
  const stoppedRef = useRef<Operation | null>(null);
  // L3: the running agent's live output and whether the user has scrolled
  // away from the bottom (in which case new output must not yank them back).
  const [agentOutput, setAgentOutput] = useState<{ state?: string; running: boolean; output: string } | null>(null);
  const [outputOpen, setOutputOpen] = useState(true);
  const preRef = useRef<HTMLPreElement | null>(null);
  const stickRef = useRef(true);
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
      stoppedRef.current = null;
    } else if (stoppedRef.current) {
      // C3: nothing is running and this card is (or is about to be)
      // showing its post-Stop state -- pick up what the daemon captured.
      // Capture happens as part of Execute's own teardown before the
      // operation itself leaves running/pending, so by the time `running`
      // above is empty this is already settled; re-fetched every poll so a
      // race with that teardown resolves within one tick rather than never.
      try {
        const guide = await get<{ partial_work?: PartialWork }>(
          `/projects/${projectId}/initiatives/${initiativeId}/guidance`,
        );
        if (gen === genRef.current) setPartialWork(guide?.partial_work ?? null);
      } catch {
        /* keep the previous value */
      }
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
    stoppedRef.current = null;
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

  // L3: poll the running agent's live output while an execute is in flight.
  // Keyed on id/type/status rather than the whole `op` object, which is a
  // new object every 2.5s poll tick even when nothing about it changed --
  // that would restart this poll (and lose the user's scroll position) every
  // cycle instead of only when the running operation actually changes.
  // Cleared -- stopping the poll -- the moment the operation is no longer
  // execute_initiative/in-flight, which also covers the card unmounting.
  useEffect(() => {
    if (!op || op.type !== "execute_initiative" || !isOperationInFlight(op.status)) {
      // Not cleared when the run stops: the stopped card below keeps the
      // last output the user was reading. A finished run's output is on its
      // change set.
      return;
    }
    // A new run starts with an empty panel, not the previous run's output.
    setAgentOutput(null);
    let cancelled = false;
    // One request at a time: with a slow Docker, stacked polls took every
    // connection the browser allows to the daemon -- Stop's included.
    let inFlight = false;
    const poll = async () => {
      if (inFlight) return;
      inFlight = true;
      try {
        const out = await get<{ state?: string; running: boolean; output: string }>(
          `/projects/${projectId}/initiatives/${initiativeId}/agent-output`,
        );
        if (!cancelled) setAgentOutput(out);
      } catch {
        /* transient error: leave the previous output on screen */
      } finally {
        inFlight = false;
      }
    };
    poll();
    const id = setInterval(poll, 2000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [op?.id, op?.type, op?.status, projectId, initiativeId]);

  // Auto-scroll the output box to the newest line unless the user scrolled
  // away from the bottom to read something earlier (shouldStickToBottom is
  // recorded on scroll, below, from measurements taken BEFORE this update).
  useEffect(() => {
    const el = preRef.current;
    if (el && stickRef.current) {
      el.scrollTop = el.scrollHeight;
    }
  }, [agentOutput?.output]);

  const handleOutputScroll = () => {
    const el = preRef.current;
    if (!el) return;
    stickRef.current = shouldStickToBottom(el.scrollTop, el.scrollHeight, el.clientHeight);
  };

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
            <div className="running-title">{stoppedMessage(partialWork)}</div>
          </div>
        </div>
        {agentOutput?.output && (
          <details className="agent-output">
            <summary>Agent output before it stopped</summary>
            <pre className="agent-output-pre">{agentOutput.output}</pre>
          </details>
        )}
      </section>
    );
  }

  const stop = async () => {
    setStopBusy(true);
    try {
      await post(`/operations/${op.id}/cancel`, {});
      setStopped(op);
      stoppedRef.current = op;
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
          <div className="running-title">{stageOrTitle(op.stage, op.type, OP_LABEL, task)}</div>
          <div className="running-sub" aria-hidden="true">
            Running for {fmtElapsed(now - Date.parse(op.created_at))} · you can leave this page
          </div>
        </div>
        <button className="running-stop" onClick={stop} disabled={stopBusy}>
          {stopBusy ? "Stopping…" : "Stop"}
        </button>
      </div>
      {/* L3: live output of the coding agent while it's actually running --
          text only (never dangerouslySetInnerHTML), open by default so it's
          not one more thing to discover. `running:false` while the operation
          itself is still in flight means the agent is done and the run's own
          build/test checks are now what's taking the time. */}
      {op.type === "execute_initiative" && agentOutput && agentOutput.state !== "starting" && agentOutput.state !== "idle" && (
        <details
          className="agent-output"
          open={outputOpen}
          onToggle={(e) => setOutputOpen(e.currentTarget.open)}
        >
          <summary>Agent output</summary>
          {/* Only once the agent has really finished. "Not running" also
              covered the planner call and the workspace copy before it
              started, and the card claimed it was done. */}
          {agentOutput.state === "finished" && (
            <p className="muted agent-output-note">The agent finished; running checks…</p>
          )}
          <pre ref={preRef} className="agent-output-pre" onScroll={handleOutputScroll}>
            {agentOutput.output || "(no output yet)"}
          </pre>
        </details>
      )}
    </section>
  );
}
