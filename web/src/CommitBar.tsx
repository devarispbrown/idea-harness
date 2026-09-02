export default function CommitBar({
  explanation,
  actionLabel,
  onAction,
}: {
  explanation: string;
  actionLabel: string;
  onAction: () => void;
}) {
  return (
    <div className="commit-bar">
      <span className="commit-explanation">{explanation}</span>
      <button className="commit-action" onClick={onAction}>
        {actionLabel}
      </button>
    </div>
  );
}
