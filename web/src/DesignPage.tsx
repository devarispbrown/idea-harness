import { useState } from "react";
import { post } from "./api";
import PhasePage from "./PhasePage";
import type { StatusPill } from "./PhaseHeader";
import type { DocumentRowProps } from "./DocumentRow";

const STAGES = [
  { value: "architecture", label: "Architecture" },
  { value: "product_design", label: "Product design" },
  { value: "verification_design", label: "Verification design" },
];

// DesignPage shows the design artifacts and, once a plan exists and nothing has
// executed, a "reopen a design stage" cascade (re-runs that stage + downstream).
export default function DesignPage({
  projectId,
  initiativeId,
  pill,
  docs,
  canReopen,
}: {
  projectId: string;
  initiativeId: string;
  pill?: StatusPill;
  docs: DocumentRowProps[];
  canReopen: boolean;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState("");

  const reopen = async (phase: string) => {
    if (busy) return;
    setBusy(phase);
    setError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/reopen-phase`, { phase });
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <PhasePage
      title="Design"
      purpose="How it will be built, and how we'll know it works."
      pill={pill}
      docs={docs}
    >
      {canReopen && (
        <section className="reopen-card">
          <div className="reopen-title">Reopen a design stage</div>
          <div className="reopen-desc">
            Re-runs that stage and everything after it — the plan regenerates. Refused once execution has begun.
          </div>
          <div className="reopen-actions">
            {STAGES.map((s) => (
              <button
                key={s.value}
                className="cs-secondary"
                onClick={() => reopen(s.value)}
              >
                {busy === s.value ? "Reopening…" : s.label}
              </button>
            ))}
          </div>
          {error && <div className="failed-error">{error}</div>}
        </section>
      )}
    </PhasePage>
  );
}
