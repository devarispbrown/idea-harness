export default function CommitBar({
  explanation,
  actionLabel,
  onAction,
  secondary,
}: {
  explanation: string;
  actionLabel: string;
  onAction: () => void;
  secondary?: { label: string; onAction: () => void };
}) {
  return (
    <div className="commit-bar">
      <span className="commit-explanation">{explanation}</span>
      {secondary && (
        <button className="commit-secondary" onClick={secondary.onAction}>
          {secondary.label}
        </button>
      )}
      <button className="commit-action" onClick={onAction}>
        {actionLabel}
      </button>
    </div>
  );
}
