import { useState } from "react";
import LineageChip from "./LineageChip";

export interface DecisionItem {
  id: string;
  code: string;
  question: string;
  recommendation: string;
  options: string[];
  status: string;
  resolution?: string;
  // "product" | "architecture" | "design" | "security" | "operations" (§19).
  // Only "product" decisions are PRD decisions; the rest are raised during
  // Design and are scoped there, not onto Define (D1/judgmentScope.ts).
  type?: string;
}
export interface FindingItem {
  id: string;
  severity: string;
  finding: string;
  recommendation: string;
  disposition?: string;
  // What the review that produced this finding looked at: "prd" or
  // "requirement" for the PRD review, "execution_plan" for the plan review,
  // "change_set" for Build's per-task review. Used to scope findings to the
  // right phase (D1/judgmentScope.ts).
  target_type?: string;
}
export interface QuestionItem {
  id: string;
  question: string;
  status: string;
}

export const DISPOSITIONS: { value: string; label: string }[] = [
  { value: "mitigate", label: "Fix it" },
  { value: "accepted_risk", label: "Accept risk" },
  { value: "not_applicable", label: "Not applicable" },
  { value: "deferred", label: "Defer" },
  { value: "fixed", label: "Fixed" },
  { value: "duplicate", label: "Duplicate" },
  { value: "rejected", label: "Rejected" },
];
const DISPOSITIONS_VISIBLE = DISPOSITIONS.slice(0, 4);
const DISPOSITIONS_OVERFLOW = DISPOSITIONS.slice(4);

function severityClass(sev: string): string {
  switch (sev) {
    case "critical":
    case "high":
      return "chip-failing";
    case "medium":
      return "chip-needs-you";
    default:
      return "chip-inactive";
  }
}

function IdChip({ label, className }: { label: string; className: string }) {
  return <span className={`idchip ${className}`}>{label}</span>;
}

export function DecisionCard({
  d,
  onResolve,
  onReopen,
  projectId,
  initiativeId,
}: {
  d: DecisionItem;
  onResolve: (resolution: string) => void;
  onReopen: () => void;
  projectId: string;
  initiativeId: string;
}) {
  const [writing, setWriting] = useState(false);
  const [custom, setCustom] = useState("");
  if (d.status !== "open") {
    return (
      <section className="judgment-card resolved">
        <div className="jc-head">
          <LineageChip label={d.code} className="chip-accent" projectId={projectId} initiativeId={initiativeId} nodeType="decision" nodeId={d.id} />
          <span className="jc-title">{d.question}</span>
        </div>
        <p className="jc-resolution">
          Resolved: <strong>{d.resolution || "(no resolution)"}</strong>
        </p>
        <button className="jc-ghost" onClick={onReopen}>
          Undo
        </button>
      </section>
    );
  }
  const others = (d.options ?? []).filter((o) => o !== d.recommendation);
  return (
    <section className="judgment-card">
      <div className="jc-head">
        <LineageChip label={d.code} className="chip-accent" projectId={projectId} initiativeId={initiativeId} nodeType="decision" nodeId={d.id} />
        <span className="jc-title">{d.question}</span>
      </div>
      {d.recommendation && (
        <p className="jc-rec">
          Recommendation: <strong>{d.recommendation}</strong>
        </p>
      )}
      <div className="jc-actions">
        {d.recommendation && (
          <button className="jc-primary" onClick={() => onResolve(d.recommendation)}>
            ✓ {d.recommendation}
          </button>
        )}
        {others.map((o) => (
          <button key={o} className="jc-secondary" onClick={() => onResolve(o)}>
            {o}
          </button>
        ))}
        <button className="jc-secondary" onClick={() => setWriting((v) => !v)}>
          Write my own…
        </button>
      </div>
      {writing && (
        <div className="jc-own">
          <input
            className="text-input"
            placeholder="Your resolution…"
            value={custom}
            onChange={(e) => setCustom(e.target.value)}
          />
          <button
            className="jc-primary"
            onClick={() => custom.trim() && onResolve(custom.trim())}
          >
            Resolve
          </button>
        </div>
      )}
    </section>
  );
}

export function FindingCard({
  f,
  onDisposition,
}: {
  f: FindingItem;
  onDisposition: (disposition: string) => void;
}) {
  const [open, setOpen] = useState(false);
  if (f.disposition) {
    const label = DISPOSITIONS.find((d) => d.value === f.disposition)?.label ?? f.disposition;
    return (
      <section className="judgment-card resolved">
        <div className="jc-head">
          <IdChip label={f.severity.toUpperCase()} className={severityClass(f.severity)} />
          <span className="jc-title">{f.finding}</span>
        </div>
        <p className="jc-resolution">
          <span className="jc-disp">{label}</span>
        </p>
      </section>
    );
  }
  return (
    <section className="judgment-card">
      <div className="jc-head">
        <IdChip label={f.severity.toUpperCase()} className={severityClass(f.severity)} />
        <span className="jc-title">{f.finding}</span>
      </div>
      {f.recommendation && (
        <p className="jc-rec">
          Recommendation: <strong>{f.recommendation}</strong>
        </p>
      )}
      <div className="jc-actions">
        {DISPOSITIONS_VISIBLE.map((d) => (
          <button
            key={d.value}
            className={d.value === "mitigate" ? "jc-primary" : "jc-secondary"}
            onClick={() => onDisposition(d.value)}
          >
            {d.label}
          </button>
        ))}
        <button className="jc-secondary" onClick={() => setOpen((v) => !v)}>
          …
        </button>
        {open && (
          <span className="jc-overflow">
            {DISPOSITIONS_OVERFLOW.map((d) => (
              <button key={d.value} className="jc-secondary" onClick={() => onDisposition(d.value)}>
                {d.label}
              </button>
            ))}
          </span>
        )}
      </div>
    </section>
  );
}

export function QuestionCard({
  q,
  suggested,
  onAnswer,
  onSuggest,
}: {
  q: QuestionItem;
  suggested?: string;
  onAnswer: (answer: string) => void;
  onSuggest: () => void;
}) {
  const [answer, setAnswer] = useState("");
  if (q.status !== "open") {
    return null;
  }
  return (
    <section className="judgment-card">
      <div className="jc-head">
        <IdChip label={`Q-${q.id.slice(0, 6).toUpperCase()}`} className="chip-inactive" />
        <span className="jc-title">{q.question}</span>
      </div>
      {suggested && !answer.trim() && (
        <div className="jc-suggested">
          Suggested: <strong>{suggested}</strong>
          <button className="jc-ghost" onClick={() => setAnswer(suggested)}>
            Use
          </button>
        </div>
      )}
      <div className="jc-ask">
        <input
          className="text-input"
          placeholder="Your answer…"
          value={answer}
          onChange={(e) => setAnswer(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && answer.trim()) onAnswer(answer.trim());
          }}
        />
        <button className="jc-secondary" onClick={onSuggest}>
          ✨ Suggest
        </button>
        {answer.trim() && (
          <button className="jc-primary" onClick={() => onAnswer(answer.trim())}>
            Answer
          </button>
        )}
      </div>
    </section>
  );
}
