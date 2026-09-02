import type { ReactNode } from "react";
import PhaseHeader, { type StatusPill } from "./PhaseHeader";
import DocumentRow, { type DocumentRowProps } from "./DocumentRow";
import CommitBar from "./CommitBar";

export default function PhasePage({
  title,
  purpose,
  pill,
  docs,
  children,
  commit,
}: {
  title: string;
  purpose: string;
  pill?: StatusPill;
  docs?: DocumentRowProps[];
  children?: ReactNode;
  commit?: {
    explanation: string;
    actionLabel: string;
    onAction: () => void;
    secondary?: { label: string; onAction: () => void };
  };
}) {
  return (
    <div className="phase-page">
      <PhaseHeader title={title} purpose={purpose} pill={pill} />
      {docs && docs.length > 0 && (
        <>
          <div className="doc-eyebrow">Produced so far</div>
          <div className="doc-list">
            {docs.map((d, i) => (
              <DocumentRow key={i} {...d} />
            ))}
          </div>
        </>
      )}
      {children}
      {commit && (
        <CommitBar
          explanation={commit.explanation}
          actionLabel={commit.actionLabel}
          onAction={commit.onAction}
          secondary={commit.secondary}
        />
      )}
    </div>
  );
}
