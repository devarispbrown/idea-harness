import { useEffect, useState } from "react";
import { get, resource } from "./api";
import { derivePhases, PHASE_LABELS } from "./phases";
import type { PhaseName } from "./router";

interface Initiative {
  id: string;
  title: string;
  description: string;
  status: string;
}
interface Artifact {
  kind: string;
  version: number;
}
interface Task {
  status: string;
}
interface ChangeSet {
  status: string;
}

const STATUS_PILL: Record<string, { label: string; state: string }> = {
  running: { label: "running", state: "running" },
  needs_human_judgment: { label: "needs you", state: "needs-you" },
  approved: { label: "approved", state: "verified" },
  plan_approved: { label: "plan approved", state: "verified" },
  complete: { label: "shipped", state: "verified" },
  failed: { label: "failed", state: "failing" },
};

export default function ProductHome({
  projectId,
  initiatives,
  onOpen,
  onCreate,
}: {
  projectId: string;
  initiatives: Initiative[];
  onOpen: (id: string) => void;
  onCreate: (idea: string) => void;
}) {
  const [rows, setRows] = useState<Record<string, { phases: { phase: PhaseName; state: string }[] }>>({});
  const [idea, setIdea] = useState("");

  useEffect(() => {
    let alive = true;
    (async () => {
      const out: Record<string, { phases: { phase: PhaseName; state: string }[] }> = {};
      await Promise.all(
        initiatives.map(async (ini) => {
          const [a, t, c] = await Promise.all([
            get<Artifact[]>(resource(projectId, ini.id, "artifacts")).catch(() => []),
            get<Task[]>(`/projects/${projectId}/initiatives/${ini.id}/tasks`).catch(() => []),
            get<ChangeSet[]>(resource(projectId, ini.id, "change-sets")).catch(() => []),
          ]);
          const states = derivePhases({
            status: ini.status,
            artifacts: a ?? [],
            tasks: t ?? [],
            changeSets: c ?? [],
            verifiedRequirements: 0,
          });
          out[ini.id] = { phases: states.map((s) => ({ phase: s.phase, state: s.state })) };
        }),
      );
      if (alive) setRows(out);
    })();
    return () => {
      alive = false;
    };
  }, [projectId, initiatives]);

  const needYou = initiatives.filter((i) => i.status === "needs_human_judgment").length;

  return (
    <div className="product-home">
      <header className="ph-topbar">
        <span className="pn-logo-mark">ih</span>
        <span className="ph-project">my-product</span>
        <span className="ph-settings">Settings</span>
        <button className="ph-new" onClick={() => idea.trim() && onCreate(idea.trim())}>+ New idea</button>
      </header>
      <main className="ph-main">
        <div className="ph-head">
          <h1 className="ph-title">Initiatives</h1>
          <span className="muted">{initiatives.length} total</span>
          {needYou > 0 && <span className="ph-need">{needYou} need you</span>}
        </div>
        <div className="ph-create">
          <input
            className="text-input"
            placeholder="What are you thinking about building?"
            value={idea}
            onChange={(e) => setIdea(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && idea.trim() && onCreate(idea.trim())}
          />
        </div>
        <div className="ph-list">
          {initiatives.map((i, idx) => {
            const pill = STATUS_PILL[i.status];
            const phases = rows[i.id]?.phases ?? [];
            return (
              <section key={i.id} className="ph-card" onClick={() => onOpen(i.id)}>
                <div className="ph-card-head">
                  <span className="ph-num">#{idx + 1}</span>
                  <span className="ph-card-title">{i.title}</span>
                  {pill && <span className={`phase-pill pill-${pill.state}`}>{pill.label}</span>}
                </div>
                <div className="ph-bars">
                  {phases.map((p) => (
                    <div key={p.phase} className="ph-bar">
                      <span className={`ph-bar-fill ${p.state}`} />
                      <span className={`ph-bar-label ${p.state}`}>{PHASE_LABELS[p.phase]}</span>
                    </div>
                  ))}
                </div>
              </section>
            );
          })}
          {initiatives.length === 0 && <p className="muted">No initiatives yet — start one above.</p>}
        </div>
      </main>
    </div>
  );
}
