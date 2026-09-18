import { useState } from "react";
import { derivePhases, PHASE_LABELS, PHASE_PURPOSE, rollupPhaseSource } from "./phases";
import type { InitiativeRollup } from "./phases";
import { PHASES, type PhaseName } from "./router";

interface Initiative {
  id: string;
  title: string;
  description: string;
  status: string;
}

// "shipped" asserted every complete initiative was a real deploy, the same
// overclaim buildLogic.ts's appliedOutcomeLabel exists to avoid on Build's
// own change-set pills (G3) -- a demo/placeholder-executor initiative that
// never touched anything real read exactly like one that did. ProductHome's
// rollup carries no per-change-set model/executor detail (that lives on
// BuildPage's own change-set list), so there is no cheap way to tell the two
// apart here; "Complete" claims only what's actually known at this level.
const STATUS_PILL: Record<string, { label: string; state: string }> = {
  running: { label: "running", state: "running" },
  needs_human_judgment: { label: "needs you", state: "needs-you" },
  approved: { label: "approved", state: "verified" },
  plan_approved: { label: "plan approved", state: "verified" },
  complete: { label: "Complete", state: "verified" },
  failed: { label: "failed", state: "failing" },
};

export default function ProductHome({
  initiatives,
  attention,
  projectName,
  onOpen,
  onCreate,
  onOpenSettings,
}: {
  initiatives: Initiative[];
  attention: InitiativeRollup[];
  projectName: string;
  onOpen: (id: string) => void;
  onCreate: (idea: string) => void;
  onOpenSettings: () => void;
}) {
  const [idea, setIdea] = useState("");
  // Shown once, after an empty "+ New idea" / Enter, until the field has
  // something in it again -- clicking with nothing typed used to just do
  // nothing (K).
  const [ideaHint, setIdeaHint] = useState(false);
  const rollupById = new Map(attention.map((r) => [r.id, r]));

  const needYou = initiatives.filter((i) => i.status === "needs_human_judgment").length;

  const submitIdea = () => {
    if (idea.trim()) onCreate(idea.trim());
    else setIdeaHint(true);
  };

  return (
    <div className="product-home">
      <header className="ph-topbar">
        <span className="pn-logo-mark">ih</span>
        {/* The literal string "my-product" used to show here regardless of
            which project was open. */}
        <span className="ph-project">{projectName || "Project"}</span>
        <button className="ph-settings" onClick={onOpenSettings}>
          Settings
        </button>
        <button className="ph-new" onClick={submitIdea}>+ New idea</button>
      </header>
      {/* A single <main> landmark per page -- App.tsx already renders one
          around this component, so this is a plain div (K). */}
      <div className="ph-main">
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
            onChange={(e) => {
              setIdea(e.target.value);
              if (ideaHint) setIdeaHint(false);
            }}
            onKeyDown={(e) => e.key === "Enter" && submitIdea()}
          />
          {ideaHint && (
            <p className="ph-create-hint">
              Type what you're thinking about building, then press Enter — e.g. "Add CSV export to
              the reports page".
            </p>
          )}
        </div>
        {initiatives.length === 0 ? (
          <ProductHomeExplainer />
        ) : (
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
          </div>
        )}
      </div>
    </div>
  );
}

// ProductHomeExplainer is the first-run empty state (G4): before this, an
// empty project showed nothing but "No initiatives yet — start one above.",
// with no explanation of what the six phases mean or what happens next.
function ProductHomeExplainer() {
  return (
    <div className="ph-empty">
      <p className="ph-empty-lead">
        Every idea moves through the same six phases, in order, from a rough idea to something
        verified against its own requirements:
      </p>
      <ol className="ph-empty-phases">
        {PHASES.map((phase) => (
          <li key={phase} className="ph-empty-phase">
            <span className="ph-empty-phase-name">{PHASE_LABELS[phase]}</span>
            <span className="ph-empty-phase-purpose">{PHASE_PURPOSE[phase]}</span>
          </li>
        ))}
      </ol>
      <p className="ph-empty-next">
        <strong>Next:</strong> type what you're thinking about building above, then press Enter.
      </p>
    </div>
  );
}
