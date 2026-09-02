import { useState } from "react";
import { derivePhases, PHASE_LABELS, rollupPhaseSource } from "./phases";
import type { InitiativeRollup } from "./phases";
import type { PhaseName } from "./router";

interface Initiative {
  id: string;
  title: string;
  description: string;
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
  initiatives,
  attention,
  onOpen,
  onCreate,
}: {
  initiatives: Initiative[];
  attention: InitiativeRollup[];
  onOpen: (id: string) => void;
  onCreate: (idea: string) => void;
}) {
  const [idea, setIdea] = useState("");
  const rollupById = new Map(attention.map((r) => [r.id, r]));

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
            const rollup = rollupById.get(i.id);
            const phases: { phase: PhaseName; state: string }[] = rollup
              ? derivePhases(rollupPhaseSource(rollup)).map((s) => ({ phase: s.phase, state: s.state }))
              : [];
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
