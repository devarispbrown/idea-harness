import { useCallback, useEffect, useState } from "react";
import { get, post, resource, token, unwrapError } from "./api";
import PhasePage from "./PhasePage";
import { ARTIFACT_TITLES } from "./phases";
import { navigate } from "./router";
import type { StatusPill } from "./PhaseHeader";
import type { DocumentRowProps } from "./DocumentRow";
import { FindingCard, type FindingItem } from "./JudgmentCard";
import { findingsForPlan } from "./judgmentScope";
import { planTaskActions } from "./judgmentCopy";
import LineagePanel from "./LineagePanel";
import ReviewConvergence from "./ReviewConvergence";
import NoteField from "./NoteField";
import { isOperationInFlight } from "./buildLogic";
import { revisionOutcome, type PlanSnapshot } from "./planRevision";

interface Task {
  id: string;
  code: string;
  title: string;
  statement: string;
  status: string;
  priority: string;
  estimate: string;
  // requirement_ids / depends_on: not yet documented in docs/openapi.yaml's
  // Task schema (contract assumption -- see the wave-3 report). Optional so
  // requirement chips and the dependencies editor both degrade to "nothing
  // selected" rather than breaking against a daemon that doesn't echo them
  // back on GET/POST .../tasks yet.
  requirement_ids?: string[];
  depends_on?: string[];
}
interface Artifact {
  id: string;
  kind: string;
  version: number;
}
interface Requirement {
  id: string;
  code: string;
  statement: string;
}

const emptyDraft = () => ({ title: "", statement: "", priority: "should", estimate: "", requirementIds: [] as string[] });

export default function PlanPage({
  projectId,
  initiativeId,
  status,
  judgmentReason,
}: {
  projectId: string;
  initiativeId: string;
  status: string;
  judgmentReason?: string;
}) {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [findings, setFindings] = useState<FindingItem[]>([]);
  const [requirements, setRequirements] = useState<Requirement[]>([]);
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState(emptyDraft());
  const [adding, setAdding] = useState(false);
  const [hasChangeSets, setHasChangeSets] = useState(false);
  const [error, setError] = useState("");
  // "Ask for a revision" takes a note: null while the form is closed, the
  // draft while it is open. It used to post straight away with no input, so
  // the planner regenerated from exactly what produced the plan in hand.
  const [revisionNote, setRevisionNote] = useState<string | null>(null);
  // The revision in flight, with the plan as it stood when it was asked for,
  // so the page can say what the revision changed -- including "nothing" --
  // rather than leaving the reader to compare version numbers.
  const [pendingRevision, setPendingRevision] = useState<{ opId: string; before: PlanSnapshot } | null>(null);
  const [revisionResult, setRevisionResult] = useState("");

  // Merge (review B3, task 6): pick exactly two tasks, then choose which one
  // survives.
  const [mergeMode, setMergeMode] = useState(false);
  const [mergeSelected, setMergeSelected] = useState<string[]>([]);
  const [mergeSurvivor, setMergeSurvivor] = useState<string | null>(null);
  const [mergeBusy, setMergeBusy] = useState(false);
  const [mergeError, setMergeError] = useState("");

  // Split (review B3, task 6): a small form for exactly two replacement
  // tasks, each with its own title/statement/requirements.
  const [splitTarget, setSplitTarget] = useState<string | null>(null);
  const [splitDraft, setSplitDraft] = useState([
    { title: "", statement: "", requirementIds: [] as string[] },
    { title: "", statement: "", requirementIds: [] as string[] },
  ]);
  const [splitBusy, setSplitBusy] = useState(false);
  const [splitError, setSplitError] = useState("");

  // Dependencies (review B3, task 6): a multi-select of every other task.
  const [depsTarget, setDepsTarget] = useState<string | null>(null);
  const [depsSelected, setDepsSelected] = useState<string[]>([]);
  const [depsBusy, setDepsBusy] = useState(false);
  const [depsError, setDepsError] = useState<Record<string, string>>({});

  const load = useCallback(async () => {
    const [t, a, c, f, r] = await Promise.all([
      get<Task[]>(`/projects/${projectId}/initiatives/${initiativeId}/tasks`).catch(() => []),
      get<Artifact[]>(resource(projectId, initiativeId, "artifacts")).catch(() => []),
      // Change sets, because both plan-level actions are refused once one
      // exists — revising a plan whose work has started would orphan it.
      get<{ id: string }[]>(resource(projectId, initiativeId, "change-sets")).catch(() => []),
      // D1: the plan-review findings belong here, not on Define — see
      // judgmentScope.ts for why target_type is what tells them apart.
      get<FindingItem[]>(resource(projectId, initiativeId, "findings")).catch(() => []),
      // Requirement codes/statements for the "+ Add a task" and split forms'
      // multi-selects, and for each task's requirement chips.
      get<Requirement[]>(resource(projectId, initiativeId, "requirements")).catch(() => []),
    ]);
    setTasks(t ?? []);
    setArtifacts(a ?? []);
    setHasChangeSets((c ?? []).length > 0);
    setFindings(f ?? []);
    setRequirements(r ?? []);
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
    const id = setInterval(load, 2500);
    return () => clearInterval(id);
  }, [load]);

  // Follow the revision's operation; once it settles, reload and say what it
  // changed against the snapshot taken when it was asked for.
  useEffect(() => {
    if (!pendingRevision) return;
    let cancelled = false;
    const poll = async () => {
      let op: { status: string; error?: string } | null = null;
      try {
        op = await get<{ status: string; error?: string }>(`/operations/${pendingRevision.opId}`);
      } catch {
        return; // transient; keep polling
      }
      if (cancelled || !op || isOperationInFlight(op.status)) return;
      const [t, a] = await Promise.all([
        get<Task[]>(`/projects/${projectId}/initiatives/${initiativeId}/tasks`).catch(() => null),
        get<Artifact[]>(resource(projectId, initiativeId, "artifacts")).catch(() => null),
      ]);
      if (cancelled) return;
      const versions = (a ?? []).filter((x) => x.kind === "execution_plan").map((x) => x.version);
      setRevisionResult(
        op.status === "succeeded"
          ? revisionOutcome(pendingRevision.before, {
              version: versions.length ? Math.max(...versions) : 0,
              codes: (t ?? []).map((x) => x.code),
            })
          : `The revision did not finish (${op.status})${op.error ? `: ${op.error}` : "."}`,
      );
      setPendingRevision(null);
      await load();
    };
    poll();
    const id = setInterval(poll, 1500);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [pendingRevision, projectId, initiativeId, load]);

  const planVersions = artifacts.filter((a) => a.kind === "execution_plan");
  const planVersion = planVersions.length ? Math.max(...planVersions.map((a) => a.version)) : 0;
  const plan = planVersions.find((a) => a.version === planVersion);
  const sortedPlan = [...planVersions].sort((a, b) => a.version - b.version);
  const prevPlan = sortedPlan.length >= 2 ? sortedPlan[sortedPlan.length - 2] : undefined;

  const update = async (id: string, body: Record<string, string>) => {
    setError("");
    const res = await fetch(`/api/v1/projects/${projectId}/initiatives/${initiativeId}/tasks/${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json", "X-Idea-Token": token },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      const t = await res.text();
      setError(unwrapError(t, res.status, res.statusText));
      return;
    }
    setEditing(null);
    await load();
  };
  // D2: deleting a task is not undoable, so confirm before sending it —
  // unlike Cancel, which just marks status and stays reversible.
  const remove = async (id: string, title: string) => {
    if (!window.confirm(`Delete task "${title}"? This cannot be undone.`)) {
      return;
    }
    setError("");
    const res = await fetch(`/api/v1/projects/${projectId}/initiatives/${initiativeId}/tasks/${id}`, {
      method: "DELETE",
      headers: { "X-Idea-Token": token },
    });
    if (!res.ok) {
      const t = await res.text();
      // task 6: the ICP journey hit "a raw JSON error on delete" here -- the
      // backend's 409 already names every dependent task readably
      // ("TASK-1 cannot be deleted: TASK-3, TASK-4 depend on it. Remove
      // those dependencies first, or delete them too."); unwrapError is what
      // actually surfaces that sentence instead of the raw `{"error":...}`
      // body.
      setError(unwrapError(t, res.status, res.statusText));
      return;
    }
    await load();
  };
  const dispositionFinding = async (id: string, disposition: string) => {
    await post(`/projects/${projectId}/findings/${id}/disposition`, { disposition });
    await load();
  };
  // Every mutation reports its refusal instead of throwing into the void: a
  // 409 from a state this page did not rule out was an unhandled error and
  // a click that did nothing.
  const guarded = async (fn: () => Promise<unknown>) => {
    setError("");
    try {
      await fn();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
    await load();
  };
  const cancel = (id: string) =>
    guarded(() => post(`/projects/${projectId}/initiatives/${initiativeId}/tasks/${id}/cancel`, {}));
  const add = () =>
    guarded(async () => {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/tasks`, {
        title: draft.title,
        statement: draft.statement,
        priority: draft.priority,
        estimate: draft.estimate,
        requirement_ids: draft.requirementIds,
      });
      setDraft(emptyDraft());
      setAdding(false);
    });
  const move = (index: number, dir: -1 | 1) => {
    const next = [...tasks];
    const j = index + dir;
    if (j < 0 || j >= next.length) return Promise.resolve();
    [next[index], next[j]] = [next[j], next[index]];
    return guarded(() =>
      post(`/projects/${projectId}/initiatives/${initiativeId}/tasks/reorder`, { task_ids: next.map((t) => t.id) }),
    );
  };
  // Both of these refuse with a 409 in states this page cannot always rule out,
  // and `post` throws on any non-2xx. Without the catch the rejection was
  // unhandled, `load()` never ran, and the click was a silent no-op -- including
  // for the refusal whose message names `idea revise-plan` as the way out, which
  // is the one the user most needs to read.
  const approve = async () => {
    setError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/approve-plan`, {});
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return;
    }
    await load();
  };
  const snapshot = (): PlanSnapshot => ({ version: planVersion, codes: tasks.map((t) => t.code) });
  const revisePlan = async () => {
    setError("");
    setRevisionResult("");
    const note = (revisionNote ?? "").trim();
    try {
      const res = await post<{ operation_id: string }>(
        `/projects/${projectId}/initiatives/${initiativeId}/revise-plan`,
        note ? { note } : {},
      );
      setPendingRevision({ opId: res.operation_id, before: snapshot() });
      setRevisionNote(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return;
    }
    await load();
  };
  const openRevision = () => {
    setRevisionResult("");
    setRevisionNote((n) => n ?? "");
  };

  // ---- task 6: merge ----------------------------------------------------

  const toggleMergeSelect = (id: string) => {
    setMergeError("");
    setMergeSelected((sel) => {
      if (sel.includes(id)) return sel.filter((x) => x !== id);
      if (sel.length >= 2) return sel; // exactly two at a time
      const next = [...sel, id];
      if (next.length === 2) setMergeSurvivor(next[0]);
      return next;
    });
  };
  const cancelMerge = () => {
    setMergeMode(false);
    setMergeSelected([]);
    setMergeSurvivor(null);
    setMergeError("");
  };
  const doMerge = async () => {
    if (mergeSelected.length !== 2 || !mergeSurvivor) return;
    const other = mergeSelected.find((id) => id !== mergeSurvivor);
    if (!other) return;
    setMergeBusy(true);
    setMergeError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/tasks/merge`, {
        task_ids: [mergeSurvivor, other],
      });
      cancelMerge();
      await load();
    } catch (e) {
      setMergeError(e instanceof Error ? e.message : String(e));
    } finally {
      setMergeBusy(false);
    }
  };

  // ---- task 6: split ------------------------------------------------------

  const openSplit = (t: Task) => {
    setSplitTarget(t.id);
    setSplitError("");
    setSplitDraft([
      { title: "", statement: "", requirementIds: t.requirement_ids ?? [] },
      { title: "", statement: "", requirementIds: t.requirement_ids ?? [] },
    ]);
  };
  const closeSplit = () => {
    setSplitTarget(null);
    setSplitError("");
  };
  const updateSplitDraft = (index: 0 | 1, field: "title" | "statement", value: string) => {
    setSplitDraft((d) => {
      const next = [...d] as typeof d;
      next[index] = { ...next[index], [field]: value };
      return next;
    });
  };
  const toggleSplitRequirement = (index: 0 | 1, reqId: string) => {
    setSplitDraft((d) => {
      const next = [...d] as typeof d;
      const cur = next[index].requirementIds;
      next[index] = {
        ...next[index],
        requirementIds: cur.includes(reqId) ? cur.filter((x) => x !== reqId) : [...cur, reqId],
      };
      return next;
    });
  };
  const submitSplit = async () => {
    if (!splitTarget) return;
    if (!splitDraft[0].title.trim() || !splitDraft[1].title.trim()) {
      setSplitError("Both replacement tasks need a title.");
      return;
    }
    setSplitBusy(true);
    setSplitError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/tasks/${splitTarget}/split`, {
        tasks: splitDraft.map((t) => ({
          title: t.title,
          statement: t.statement,
          requirement_ids: t.requirementIds,
        })),
      });
      closeSplit();
      await load();
    } catch (e) {
      setSplitError(e instanceof Error ? e.message : String(e));
    } finally {
      setSplitBusy(false);
    }
  };

  // ---- task 6: dependencies -----------------------------------------------

  const openDeps = (t: Task) => {
    setDepsTarget(t.id);
    setDepsSelected(t.depends_on ?? []);
    setDepsError((e) => ({ ...e, [t.id]: "" }));
  };
  const closeDeps = () => setDepsTarget(null);
  const toggleDepsSelect = (id: string) => {
    setDepsSelected((sel) => (sel.includes(id) ? sel.filter((x) => x !== id) : [...sel, id]));
  };
  const saveDeps = async (taskId: string) => {
    setDepsBusy(true);
    setDepsError((e) => ({ ...e, [taskId]: "" }));
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/tasks/${taskId}/depends-on`, {
        depends_on: depsSelected,
      });
      closeDeps();
      await load();
    } catch (e) {
      // task 6: a cycle refusal ("that dependency would create a cycle: ...")
      // is already a readable sentence from the backend -- shown as-is,
      // inline, rather than swallowed or replaced with a generic message.
      setDepsError((prev) => ({ ...prev, [taskId]: e instanceof Error ? e.message : String(e) }));
    } finally {
      setDepsBusy(false);
    }
  };

  const docs: DocumentRowProps[] = plan
    ? [
        {
          title: `${ARTIFACT_TITLES.execution_plan} · v${plan.version}`,
          meta: "",
          onRead: () => navigate(`#/i/${initiativeId}/doc/${plan.id}/${plan.version}`),
          ...(prevPlan
            ? {
                onDiff: () => navigate(`#/i/${initiativeId}/diff/${prevPlan.id}/${plan.id}`),
                diffLabel: `Diff v${prevPlan.version} → v${plan.version}`,
              }
            : {}),
        },
      ]
    : [];

  // `plan_approved` used to render the approve button too, and after approve-plan
  // became a real accept that was wrong in both directions: the pill told the
  // user a plan already approved still needed approval, and the button behind it
  // posted an accept the daemon now refuses.
  //
  // The reason, not just the status. All four judgment reasons share
  // `needs_human_judgment`, and the API only accepts `plan_review` here — an
  // initiative parked on `plan_dependencies` was still offered "Approve plan →",
  // which only `revise-plan` can resolve. Undefined is treated as eligible:
  // rows parked before the reason was recorded carry "", and the API accepts
  // those too, so the two agree.
  //
  // This is NOT full parity with the API, which also refuses a running
  // initiative, one that already has change sets, and one carrying a structural
  // plan defect. Those are states this page cannot cheaply determine, so they
  // surface as the error the catch above now renders.
  // Once a change set exists, every plan-level command is refused by the API:
  // revising a plan whose work has started would orphan it, and approving one
  // sweeps that change set's findings. Both are 409s, so the page must not
  // offer a button guaranteed to fail.
  //
  // Hoisted and applied to EVERY branch. An earlier version wrote this into the
  // two needs_human_judgment branches and left it out of `plan_approved` --
  // which is the only one that matters, because plan_approved is where change
  // sets appear (`execute` runs from there), while no ordinary path reaches
  // needs_human_judgment with change sets present. The comment claimed the bar
  // went away; it did not.
  const planCommandsAvailable = !hasChangeSets;

  const awaitingApproval =
    status === "needs_human_judgment" &&
    (!judgmentReason || judgmentReason === "plan_review") &&
    planCommandsAvailable;

  // Parked for some other reason -- plan_dependencies, say. Approving is
  // refused there, but revise-plan is accepted from needs_human_judgment
  // whatever the reason, and `idea next` names it as the only escape. Narrowing
  // `awaitingApproval` without this branch left those states with no pill and no
  // bar at all.
  const parkedElsewhere =
    status === "needs_human_judgment" && !awaitingApproval && planCommandsAvailable;

  // An approved plan still needs a way back while one exists: this bar's action
  // is the only `revise-plan` trigger in the whole UI, so gating the bar on
  // `awaitingApproval` alone left the page with nothing to do once a plan was
  // approved. The daemon accepts it from `plan_approved` only while no change
  // set exists, so once Build starts the bar explains that instead (below)
  // and the pill keeps saying "approved": that is still the plan's state.
  const approvedIdle = status === "plan_approved" && planCommandsAvailable;

  // D1: the plan-review findings, scoped out of the initiative-wide list by
  // target_type (see judgmentScope.ts) — this is what used to render nothing
  // here while Define rendered them instead.
  const planFindings = findingsForPlan(findings);
  const openPlanFindings = planFindings.filter((f) => !f.disposition);
  const resolvedPlanFindings = planFindings.filter((f) => !!f.disposition);

  // D2: once approved, the plan is the fixed input to Build — Edit/Delete are
  // hidden so the approved document cannot be silently rewritten out from
  // under it. Reorder/Cancel are left alone; only D2 asked for these two.
  // Wave 3: merge/split/dependsOn (review B3) track reorder exactly.
  const can = planTaskActions(status);
  const tasksEditable = can.edit;

  const requirementLabel = (id: string) => requirements.find((r) => r.id === id)?.code ?? id;

  const pill: StatusPill | undefined = awaitingApproval
    ? { label: "needs approval", state: "needs-you" }
    : parkedElsewhere
      ? { label: "needs a revision", state: "needs-you" }
      : status === "plan_approved"
        ? // The plan's own state, the same on every viewport. It used to switch
          // to "building" once a change set existed, so the same plan read
          // APPROVED in one window and BUILDING in another; Build's own rail
          // entry is what says building is under way.
          { label: "approved", state: "verified" }
        : undefined;

  const commit = awaitingApproval
    ? {
        explanation:
          "Approving accepts the plan as it stands and unlocks Build. Tasks run one at a time, each pausing for review.",
        actionLabel: "Approve plan →",
        onAction: approve,
        secondary: { label: "Ask for a revision…", onAction: openRevision },
      }
    : parkedElsewhere
      ? {
          explanation: "This plan needs regenerating before it can be approved.",
          actionLabel: "Ask for a revision…",
          onAction: openRevision,
        }
      : approvedIdle
        ? {
            explanation: "This plan is approved. Build runs it task by task.",
            actionLabel: "Ask for a revision…",
            onAction: openRevision,
          }
        : status === "plan_approved" && hasChangeSets
          ? {
              // The bar used to vanish here, so the page simply lost its
              // footer once Build started (it read as missing on a phone).
              // It stays, and says why revising is closed.
              explanation:
                "This plan is approved and Build has started. Revising is closed once a change set exists.",
              actionLabel: "Go to Build →",
              onAction: () => navigate(`#/i/${initiativeId}/build`),
            }
          : undefined;

  return (
    <PhasePage title="Plan" purpose="The work, ordered. Approve it before anything runs." pill={pill} docs={docs} commit={commit}>
      {revisionNote !== null && (
        <section className="revision-request" aria-label="Ask for a revision">
          <NoteField
            id={`revise-plan-note-${initiativeId}`}
            label="What should the revised plan change?"
            placeholder="e.g. split the import step into its own task — joins the planner's inputs (optional)"
            value={revisionNote}
            onChange={setRevisionNote}
            autoFocus
          />
          <div className="revision-request-actions">
            <button className="cs-green" disabled={!!pendingRevision} onClick={revisePlan}>
              Regenerate the plan
            </button>
            <button className="cs-secondary" onClick={() => setRevisionNote(null)}>
              Cancel
            </button>
          </div>
        </section>
      )}
      {pendingRevision && <p className="muted revision-status">Regenerating the plan…</p>}
      {revisionResult && (
        <p className="revision-status" role="status">
          {revisionResult}
        </p>
      )}
      {openPlanFindings.length > 0 && (
        <div className="judgment-head">
          <h2 className="section-title">
            {openPlanFindings.length} thing{openPlanFindings.length === 1 ? "" : "s"} need
            {openPlanFindings.length === 1 ? "s" : ""} your judgment
          </h2>
          <span className="muted">
            {status === "needs_human_judgment"
              ? "the plan review raised these before it can be approved"
              : "raised by the plan review"}
          </span>
        </div>
      )}
      {openPlanFindings.length > 0 && (
        <div className="judgment-list">
          {openPlanFindings.map((f) => (
            <FindingCard key={f.id} f={f} onDisposition={(dis) => dispositionFinding(f.id, dis)} />
          ))}
        </div>
      )}
      {resolvedPlanFindings.length > 0 && (
        <>
          <div className="judgment-head">
            <h2 className="section-title">Resolved</h2>
          </div>
          <div className="judgment-list">
            {resolvedPlanFindings.map((f) => (
              <FindingCard key={f.id} f={f} onDisposition={() => {}} />
            ))}
          </div>
        </>
      )}
      {can.merge && tasks.length >= 2 && (
        <div className="plan-merge-bar">
          {!mergeMode ? (
            <button className="plan-add" style={{ width: "auto" }} onClick={() => setMergeMode(true)}>
              Merge tasks…
            </button>
          ) : (
            <>
              <span className="muted">Select two tasks to merge{mergeSelected.length > 0 ? ` (${mergeSelected.length}/2)` : ""}.</span>
              <button className="jc-secondary" onClick={cancelMerge}>Cancel</button>
            </>
          )}
        </div>
      )}
      {mergeMode && mergeSelected.length === 2 && (
        <section className="plan-merge-panel">
          <div className="muted">Merge into which task? The other's statement and requirements are folded in.</div>
          <div className="plan-merge-choices">
            {mergeSelected.map((id) => {
              const t = tasks.find((x) => x.id === id);
              return (
                <label key={id} className="plan-merge-choice">
                  <input
                    type="radio"
                    name="merge-survivor"
                    checked={mergeSurvivor === id}
                    onChange={() => setMergeSurvivor(id)}
                  />
                  {t ? `${t.code}: ${t.title}` : id}
                </label>
              );
            })}
          </div>
          <button className="jc-primary" disabled={mergeBusy} onClick={doMerge}>
            {mergeBusy ? "Merging…" : "Merge"}
          </button>
          {mergeError && <p className="error">{mergeError}</p>}
        </section>
      )}
      <section className="plan-card">
        {tasks.map((t, i) =>
          editing === t.id ? (
            <div key={t.id} className="plan-row editing">
              <input
                className="text-input"
                value={draft.title}
                onChange={(e) => setDraft((d) => ({ ...d, title: e.target.value }))}
                placeholder="Title"
              />
              <input
                className="text-input"
                value={draft.statement}
                onChange={(e) => setDraft((d) => ({ ...d, statement: e.target.value }))}
                placeholder="Statement"
              />
              <select
                className="text-input"
                value={draft.priority}
                onChange={(e) => setDraft((d) => ({ ...d, priority: e.target.value }))}
              >
                <option value="must">must</option>
                <option value="should">should</option>
                <option value="could">could</option>
              </select>
              <input
                className="text-input"
                style={{ maxWidth: 60 }}
                placeholder="est"
                value={draft.estimate}
                onChange={(e) => setDraft((d) => ({ ...d, estimate: e.target.value }))}
              />
              <button
                className="jc-primary"
                onClick={() =>
                  update(t.id, {
                    title: draft.title,
                    statement: draft.statement,
                    priority: draft.priority,
                    estimate: draft.estimate,
                  })
                }
              >
                Save
              </button>
              <button className="jc-secondary" onClick={() => setEditing(null)}>Cancel</button>
            </div>
          ) : splitTarget === t.id ? (
            <div key={t.id} className="plan-row plan-split-form">
              <div className="plan-split-title">Split {t.code} into two tasks</div>
              {([0, 1] as const).map((idx) => (
                <div key={idx} className="plan-split-task">
                  <input
                    className="text-input"
                    placeholder={`Task ${idx + 1} title`}
                    value={splitDraft[idx].title}
                    onChange={(e) => updateSplitDraft(idx, "title", e.target.value)}
                  />
                  <input
                    className="text-input"
                    placeholder="Statement"
                    value={splitDraft[idx].statement}
                    onChange={(e) => updateSplitDraft(idx, "statement", e.target.value)}
                  />
                  <div className="plan-req-select">
                    {requirements.map((r) => (
                      <label key={r.id} className="plan-req-option">
                        <input
                          type="checkbox"
                          checked={splitDraft[idx].requirementIds.includes(r.id)}
                          onChange={() => toggleSplitRequirement(idx, r.id)}
                        />
                        {r.code}
                      </label>
                    ))}
                  </div>
                </div>
              ))}
              <div className="plan-row-actions">
                <button className="jc-primary" disabled={splitBusy} onClick={submitSplit}>
                  {splitBusy ? "Splitting…" : "Split"}
                </button>
                <button className="jc-secondary" onClick={closeSplit}>Cancel</button>
              </div>
              {splitError && <p className="error">{splitError}</p>}
            </div>
          ) : depsTarget === t.id ? (
            <div key={t.id} className="plan-row plan-deps-form">
              <div className="plan-split-title">{t.code}'s dependencies</div>
              <div className="plan-req-select">
                {tasks
                  .filter((other) => other.id !== t.id)
                  .map((other) => (
                    <label key={other.id} className="plan-req-option">
                      <input
                        type="checkbox"
                        checked={depsSelected.includes(other.id)}
                        onChange={() => toggleDepsSelect(other.id)}
                      />
                      {other.code}: {other.title}
                    </label>
                  ))}
              </div>
              <div className="plan-row-actions">
                <button className="jc-primary" disabled={depsBusy} onClick={() => saveDeps(t.id)}>
                  {depsBusy ? "Saving…" : "Save dependencies"}
                </button>
                <button className="jc-secondary" onClick={closeDeps}>Cancel</button>
              </div>
              {/* task 6: a cycle refusal reads exactly as the backend wrote
                  it -- it already names the cycle. */}
              {depsError[t.id] && <p className="error">{depsError[t.id]}</p>}
            </div>
          ) : (
            <div key={t.id} className="plan-row">
              {mergeMode && (
                <input
                  type="checkbox"
                  checked={mergeSelected.includes(t.id)}
                  onChange={() => toggleMergeSelect(t.id)}
                  aria-label={`Select ${t.code} to merge`}
                />
              )}
              <span className="plan-grab">⠿</span>
              <span className="plan-code">{t.code}</span>
              <div className="plan-body">
                <div className="plan-title">{t.title}</div>
                {t.statement && <div className="plan-sub">{t.statement}</div>}
                {/* task 6: each task's requirements as chips. */}
                {(t.requirement_ids ?? []).length > 0 && (
                  <div className="plan-req-chips">
                    {(t.requirement_ids ?? []).map((rid) => (
                      <span key={rid} className="plan-req-chip">{requirementLabel(rid)}</span>
                    ))}
                  </div>
                )}
                <LineagePanel projectId={projectId} initiativeId={initiativeId} type="task" id={t.id} code={t.code} />
              </div>
              <span className={`plan-prio prio-${t.priority}`}>{t.priority}</span>
              <span className="plan-est">{t.estimate || "–"}</span>
              {(() => {
                // J: seven possible buttons in a row squeeze the title to one
                // word per line at narrow widths. Built once, rendered twice
                // below (flat above 600px, in a "⋯" menu under it) so the
                // wiring can't drift between the two layouts.
                const actions: { key: string; label: string; onClick: () => void }[] = [
                  ...(can.reorder
                    ? [
                        { key: "up", label: "↑", onClick: () => move(i, -1) },
                        { key: "down", label: "↓", onClick: () => move(i, 1) },
                      ]
                    : []),
                  ...(tasksEditable
                    ? [
                        {
                          key: "edit",
                          label: "Edit",
                          onClick: () => {
                            setEditing(t.id);
                            setDraft({ ...emptyDraft(), title: t.title, statement: t.statement, priority: t.priority, estimate: t.estimate });
                          },
                        },
                        { key: "delete", label: "Delete", onClick: () => remove(t.id, t.title) },
                      ]
                    : []),
                  ...(can.split ? [{ key: "split", label: "Split", onClick: () => openSplit(t) }] : []),
                  ...(can.dependsOn ? [{ key: "deps", label: "Dependencies", onClick: () => openDeps(t) }] : []),
                  ...(can.cancel && t.status !== "cancelled" && t.status !== "done"
                    ? [{ key: "cancel", label: "Cancel", onClick: () => cancel(t.id) }]
                    : []),
                ];
                return (
                  <>
                    <span className="plan-row-actions">
                      {actions.map((a) => (
                        <button key={a.key} className="plan-act" onClick={a.onClick}>
                          {a.label}
                        </button>
                      ))}
                    </span>
                    <details className="plan-actions-menu">
                      <summary aria-label="More actions">⋯</summary>
                      <div className="plan-actions-menu-list">
                        {actions.map((a) => (
                          <button key={a.key} className="plan-act" onClick={a.onClick}>
                            {a.label}
                          </button>
                        ))}
                      </div>
                    </details>
                  </>
                );
              })()}
            </div>
          ),
        )}
        {!can.add ? null : adding ? (
          <div className="plan-row editing">
            <input
              className="text-input"
              value={draft.title}
              onChange={(e) => setDraft((d) => ({ ...d, title: e.target.value }))}
              placeholder="Title"
            />
            <input
              className="text-input"
              value={draft.statement}
              onChange={(e) => setDraft((d) => ({ ...d, statement: e.target.value }))}
              placeholder="Statement"
            />
            {/* task 6: "added tasks with no requirement" -- requirement_ids
                is now required here too, same as the backend enforces. */}
            <div className="plan-req-select">
              {requirements.length === 0 && <span className="muted">No requirements yet.</span>}
              {requirements.map((r) => (
                <label key={r.id} className="plan-req-option">
                  <input
                    type="checkbox"
                    checked={draft.requirementIds.includes(r.id)}
                    onChange={() =>
                      setDraft((d) => ({
                        ...d,
                        requirementIds: d.requirementIds.includes(r.id)
                          ? d.requirementIds.filter((x) => x !== r.id)
                          : [...d.requirementIds, r.id],
                      }))
                    }
                  />
                  {r.code}
                </label>
              ))}
            </div>
            <button
              className="jc-primary"
              onClick={add}
              disabled={!draft.title.trim() || draft.requirementIds.length === 0}
            >
              Add
            </button>
            <button className="jc-secondary" onClick={() => { setAdding(false); setDraft(emptyDraft()); }}>Cancel</button>
          </div>
        ) : (
          <button
            className="plan-add"
            onClick={() => {
              setDraft(emptyDraft());
              setAdding(true);
            }}
          >
            + Add a task
          </button>
        )}
      </section>
      {error && <p className="error" style={{ marginTop: 12 }}>{error}</p>}
      <ReviewConvergence projectId={projectId} initiativeId={initiativeId} review="execution_plan" />
    </PhasePage>
  );
}
