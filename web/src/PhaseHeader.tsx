export interface StatusPill {
  label: string;
  state: "running" | "needs-you" | "verified" | "failing" | "inactive";
}

export default function PhaseHeader({
  title,
  purpose,
  pill,
  meta,
}: {
  title: string;
  purpose: string;
  pill?: StatusPill;
  meta?: string;
}) {
  return (
    <div className="phase-header">
      <h1 className="phase-title">{title}</h1>
      <span className="phase-purpose">{purpose}</span>
      {pill && (
        <span className={`phase-pill pill-${pill.state}`}>
          {pill.state === "running" && <span className="pill-spinner" aria-hidden="true" />}
          {pill.label}
        </span>
      )}
      {meta && <span className="phase-meta">{meta}</span>}
    </div>
  );
}
