import { useCallback, useEffect, useState } from "react";
import { get, post, resource, token } from "./api";
import PhasePage from "./PhasePage";
import { ARTIFACT_TITLES } from "./phases";
import { navigate } from "./router";
import type { StatusPill } from "./PhaseHeader";
import type { DocumentRowProps } from "./DocumentRow";

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
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState({ title: "", statement: "", priority: "should", estimate: "" });
  const [adding, setAdding] = useState(false);
  const [hasChangeSets, setHasChangeSets] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const [t, a, c] = await Promise.all([
      get<Task[]>(`/projects/${projectId}/initiatives/${initiativeId}/tasks`).catch(() => []),
      get<Artifact[]>(resource(projectId, initiativeId, "artifacts")).catch(() => []),
      // Change sets, because both plan-level actions are refused once one
      // exists — revising a plan whose work has started would orphan it.
      get<{ id: string }[]>(resource(projectId, initiativeId, "change-sets")).catch(() => []),
    ]);
    setTasks(t ?? []);
    setArtifacts(a ?? []);
    setHasChangeSets((c ?? []).length > 0);
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
  const remove = async (id: string) => {
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
      setError(String(e));
      return;
    }
    await load();
  };
  const revisePlan = async () => {
    setError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/revise-plan`, {});
    } catch (e) {
      setError(String(e));
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
  // Once a change set exists, every plan-level command is refused: revising a
  // plan whose work has started would orphan it, and approving one sweeps that
  // change set's findings. Both are 409s, and the page should not offer a
  // button that is guaranteed to fail.
  const awaitingApproval =
    status === "needs_human_judgment" &&
    (!judgmentReason || judgmentReason === "plan_review") &&
    !hasChangeSets;

  // An approved plan still needs a way back while one is available. This bar's
  // secondary action is the only `revise-plan` trigger in the whole UI, so
  // gating the entire bar on `awaitingApproval` left the page with no action at
  // all once a plan was approved. But the daemon accepts revise-plan from
  // `plan_approved` only while no change set exists — and `plan_approved` is
  // exactly where change sets appear, since `execute` runs from there — so the
  // bar goes away when Build has started rather than offering a certain 409.
  // Parked for some OTHER reason -- plan_dependencies, or a park this page did
  // not cause. Approving is refused by the API, but revise-plan is accepted from
  // needs_human_judgment whatever the reason, and `idea next` names it as the
  // only escape from plan_dependencies. Narrowing `awaitingApproval` without
  // this branch left those states with no pill and no bar, which is the same
  // defect the plan_approved branch below exists to fix, one state over.
  const parkedElsewhere =
    status === "needs_human_judgment" && !awaitingApproval && !hasChangeSets;

  const pill: StatusPill | undefined = awaitingApproval
    ? { label: "needs approval", state: "needs-you" }
    : parkedElsewhere
      ? { label: "needs a revision", state: "needs-you" }
      : status === "plan_approved"
        ? { label: "approved", state: "verified" }
        : undefined;

  const commit = awaitingApproval
    ? {
        explanation: "Approving accepts the plan as it stands and unlocks Build. Tasks run one at a time, each pausing for review.",
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
      : status === "plan_approved"
        ? {
            explanation: "This plan is approved. Build runs it task by task.",
            actionLabel: "Ask for a revision",
            onAction: revisePlan,
          }
        : undefined;

  return (
    <PhasePage title="Plan" purpose="The work, ordered. Approve it before anything runs." pill={pill} docs={docs} commit={commit}>
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
              <button className="plan-act" onClick={() => move(i, -1)}>↑</button>
              <button className="plan-act" onClick={() => move(i, 1)}>↓</button>
              <button
                className="plan-act"
                onClick={() => {
                  setEditing(t.id);
                  setDraft({ title: t.title, statement: t.statement, priority: t.priority, estimate: t.estimate });
                }}
              >
                Edit
              </button>
              <button className="plan-act" onClick={() => remove(t.id)}>Delete</button>
              {t.status !== "cancelled" && (
                <button className="plan-act" onClick={() => cancel(t.id)}>Cancel</button>
              )}
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
