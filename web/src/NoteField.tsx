import { isSubmitShortcut, modLabel } from "./shortcuts";

// NoteField is the one labelled free-text input every "tell the agent what to
// change" action uses: the run note on Build, Request changes, a retry, and
// Plan's revision request. They were a mix of placeholder-only textareas and no
// input at all; a visible <label> is what makes the field findable (and what a
// screen reader announces), and one component keeps the four from drifting.
//
// onSubmit makes ⌘+Enter / Ctrl+Enter send the note (the field says so under
// it); onCancel makes Escape close the form it belongs to. Plain Enter stays a
// newline: these are multi-line notes.
export default function NoteField({
  id,
  label,
  value,
  onChange,
  placeholder,
  rows = 2,
  autoFocus = false,
  onSubmit,
  onCancel,
  submitLabel = "send",
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  rows?: number;
  autoFocus?: boolean;
  onSubmit?: () => void;
  onCancel?: () => void;
  // submitLabel is the verb the shortcut hint uses ("⌘ Enter to send").
  submitLabel?: string;
}) {
  const hintId = `${id}-hint`;
  return (
    <div className="note-field">
      <label className="note-field-label" htmlFor={id}>
        {label}
      </label>
      <textarea
        id={id}
        className="text-input note-field-input"
        placeholder={placeholder}
        value={value}
        rows={rows}
        autoFocus={autoFocus}
        aria-describedby={onSubmit ? hintId : undefined}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (onSubmit && isSubmitShortcut(e.nativeEvent)) {
            e.preventDefault();
            onSubmit();
          } else if (onCancel && e.key === "Escape") {
            e.preventDefault();
            e.stopPropagation();
            onCancel();
          }
        }}
      />
      {onSubmit && (
        <span id={hintId} className="note-field-hint">
          <kbd>{modLabel()}</kbd> <kbd>Enter</kbd> to {submitLabel}
        </span>
      )}
    </div>
  );
}
