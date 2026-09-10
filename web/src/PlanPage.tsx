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
}: {
  projectId: string;
  initiativeId: string;
  status: string;
}) {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState({ title: "", statement: "", priority: "should", estimate: "" });
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const [t, a] = await Promise.all([
      get<Task[]>(`/projects/${projectId}/initiatives/${initiativeId}/tasks`).catch(() => []),
      get<Artifact[]>(resource(projectId, initiativeId, "artifacts")).catch(() => []),
    ]);
    setTasks(t ?? []);
    setArtifacts(a ?? []);
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
  const approve = async () => {
    await post(`/projects/${projectId}/initiatives/${initiativeId}/approve-plan`, {});
    await load();
  };
  const revisePlan = async () => {
    await post(`/projects/${projectId}/initiatives/${initiativeId}/revise-plan`, {});
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

  // `plan_approved` used to render this too, and after approve-plan became a
  // real accept that combination was wrong in both directions: the pill told the
  // user a plan already approved still needed approval, and the button behind it
  // posted an accept that the daemon now refuses with a 409 — which `post`
  // throws and nothing here catches, so the click was a silent no-op under a
  // label promising otherwise. The gate is the same one the API applies.
  const awaitingApproval = status === "needs_human_judgment";

  const pill: StatusPill | undefined = awaitingApproval
    ? { label: "needs approval", state: "needs-you" }
    : undefined;

  const commit = awaitingApproval
    ? {
        explanation: "Approving accepts the plan as it stands and unlocks Build. Tasks run one at a time, each pausing for review.",
        actionLabel: "Approve plan →",
        onAction: approve,
        secondary: { label: "Ask for a revision", onAction: revisePlan },
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
