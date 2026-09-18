import { useCallback, useEffect, useState } from "react";
import { get, post, resource, token } from "./api";
import PhasePage from "./PhasePage";
import { ARTIFACT_TITLES } from "./phases";
import { navigate } from "./router";
import type { StatusPill } from "./PhaseHeader";
import type { DocumentRowProps } from "./DocumentRow";
import { FindingCard, type FindingItem } from "./JudgmentCard";
import { findingsForPlan } from "./judgmentScope";
import { planTaskEditable } from "./judgmentCopy";

interface Task {
  id: string;
  code: string;
  title: string;
  statement: string;
  status: string;
  priority: string;
  estimate: string;
}
interface Artifact {
  id: string;
  kind: string;
  version: number;
}

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
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState({ title: "", statement: "", priority: "should", estimate: "" });
  const [adding, setAdding] = useState(false);
  const [hasChangeSets, setHasChangeSets] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const [t, a, c, f] = await Promise.all([
      get<Task[]>(`/projects/${projectId}/initiatives/${initiativeId}/tasks`).catch(() => []),
      get<Artifact[]>(resource(projectId, initiativeId, "artifacts")).catch(() => []),
      // Change sets, because both plan-level actions are refused once one
      // exists — revising a plan whose work has started would orphan it.
      get<{ id: string }[]>(resource(projectId, initiativeId, "change-sets")).catch(() => []),
      // D1: the plan-review findings belong here, not on Define — see
      // judgmentScope.ts for why target_type is what tells them apart.
      get<FindingItem[]>(resource(projectId, initiativeId, "findings")).catch(() => []),
    ]);
    setTasks(t ?? []);
    setArtifacts(a ?? []);
    setHasChangeSets((c ?? []).length > 0);
    setFindings(f ?? []);
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
    const id = setInterval(load, 2500);
    return () => clearInterval(id);
  }, [load]);

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
      setError(t || `edit failed (${res.status})`);
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
      setError(t || `delete failed (${res.status})`);
      return;
    }
    await load();
  };
  const dispositionFinding = async (id: string, disposition: string) => {
    await post(`/projects/${projectId}/findings/${id}/disposition`, { disposition });
    await load();
  };
  const cancel = async (id: string) => {
    await post(`/projects/${projectId}/initiatives/${initiativeId}/tasks/${id}/cancel`, {});
    await load();
  };
  const add = async () => {
    await post(`/projects/${projectId}/initiatives/${initiativeId}/tasks`, {
      title: draft.title,
      statement: draft.statement,
      priority: draft.priority,
      estimate: draft.estimate,
    });
    setDraft({ title: "", statement: "", priority: "should", estimate: "" });
    setAdding(false);
    await load();
  };
  const move = async (index: number, dir: -1 | 1) => {
    const next = [...tasks];
    const j = index + dir;
    if (j < 0 || j >= next.length) return;
    [next[index], next[j]] = [next[j], next[index]];
    await post(`/projects/${projectId}/initiatives/${initiativeId}/tasks/reorder`, {
      task_ids: next.map((t) => t.id),
    });
    await load();
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
  const revisePlan = async () => {
    setError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/revise-plan`, {});
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return;
    }
    await load();
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
  // set exists, so the bar goes away when Build starts -- and the pill says
  // "building" rather than disappearing, because the state is not an error.
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
  const tasksEditable = planTaskEditable(status);

  const pill: StatusPill | undefined = awaitingApproval
    ? { label: "needs approval", state: "needs-you" }
    : parkedElsewhere
      ? { label: "needs a revision", state: "needs-you" }
      : status === "plan_approved"
        ? { label: hasChangeSets ? "building" : "approved", state: "verified" }
        : undefined;

  const commit = awaitingApproval
    ? {
        explanation:
          "Approving accepts the plan as it stands and unlocks Build. Tasks run one at a time, each pausing for review.",
        actionLabel: "Approve plan →",
        onAction: approve,
        secondary: { label: "Ask for a revision", onAction: revisePlan },
      }
    : parkedElsewhere
      ? {
          explanation: "This plan needs regenerating before it can be approved.",
          actionLabel: "Ask for a revision",
          onAction: revisePlan,
        }
      : approvedIdle
        ? {
            explanation: "This plan is approved. Build runs it task by task.",
            actionLabel: "Ask for a revision",
            onAction: revisePlan,
          }
        : undefined;

  return (
    <PhasePage title="Plan" purpose="The work, ordered. Approve it before anything runs." pill={pill} docs={docs} commit={commit}>
      {openPlanFindings.length > 0 && (
        <div className="judgment-head">
          <h2 className="section-title">
            {openPlanFindings.length} thing{openPlanFindings.length === 1 ? "" : "s"} need
            {openPlanFindings.length === 1 ? "s" : ""} your judgment
          </h2>
          <span className="muted">the plan review raised these before it can be approved</span>
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
              <button className="jc-primary" onClick={() => update(t.id, draft)}>Save</button>
              <button className="jc-secondary" onClick={() => setEditing(null)}>Cancel</button>
            </div>
          ) : (
            <div key={t.id} className="plan-row">
              <span className="plan-grab">⠿</span>
              <span className="plan-code">{t.code}</span>
              <div className="plan-body">
                <div className="plan-title">{t.title}</div>
                {t.statement && <div className="plan-sub">{t.statement}</div>}
              </div>
              <span className={`plan-prio prio-${t.priority}`}>{t.priority}</span>
              <span className="plan-est">{t.estimate || "–"}</span>
              {(() => {
                // J: seven possible buttons in a row squeeze the title to one
                // word per line at narrow widths. Built once, rendered twice
                // below (flat above 600px, in a "⋯" menu under it) so the
                // wiring can't drift between the two layouts.
                const actions: { key: string; label: string; onClick: () => void }[] = [
                  { key: "up", label: "↑", onClick: () => move(i, -1) },
                  { key: "down", label: "↓", onClick: () => move(i, 1) },
                  ...(tasksEditable
                    ? [
                        {
                          key: "edit",
                          label: "Edit",
                          onClick: () => {
                            setEditing(t.id);
                            setDraft({ title: t.title, statement: t.statement, priority: t.priority, estimate: t.estimate });
                          },
                        },
                        { key: "delete", label: "Delete", onClick: () => remove(t.id, t.title) },
                      ]
                    : []),
                  ...(t.status !== "cancelled"
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
        {adding ? (
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
            <button className="jc-primary" onClick={add} disabled={!draft.title.trim()}>Add</button>
            <button className="jc-secondary" onClick={() => setAdding(false)}>Cancel</button>
          </div>
        ) : (
          <button
            className="plan-add"
            onClick={() => {
              setDraft({ title: "", statement: "", priority: "should", estimate: "" });
              setAdding(true);
            }}
          >
            + Add a task
          </button>
        )}
      </section>
      {error && <p className="error" style={{ marginTop: 12 }}>{error}</p>}
    </PhasePage>
  );
}
