export interface DocumentRowProps {
  title: string;
  meta: string;
  state?: "ready" | "drafting" | "superseded";
  onRead?: () => void;
  onDiff?: () => void;
  diffLabel?: string;
}

export default function DocumentRow({
  title,
  meta,
  state = "ready",
  onRead,
  onDiff,
  diffLabel,
}: DocumentRowProps) {
  return (
    <section className={`doc-row doc-${state}`}>
      <div className="doc-body">
        <div className="doc-title">{title}</div>
        {meta && <div className="doc-meta">{meta}</div>}
      </div>
      {onDiff && (
        <button className="doc-diff" onClick={onDiff}>
          {diffLabel ?? "Diff"}
        </button>
      )}
      {state === "ready" && onRead && (
        <button className="doc-read dark" onClick={onRead}>
          Read
        </button>
      )}
    </section>
  );
}
