// NoteField is the one labelled free-text input every "tell the agent what to
// change" action uses: the run note on Build, Request changes, a retry, and
// Plan's revision request. They were a mix of placeholder-only textareas and no
// input at all; a visible <label> is what makes the field findable (and what a
// screen reader announces), and one component keeps the four from drifting.
export default function NoteField({
  id,
  label,
  value,
  onChange,
  placeholder,
  rows = 2,
  autoFocus = false,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  rows?: number;
  autoFocus?: boolean;
}) {
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
        onChange={(e) => onChange(e.target.value)}
      />
    </div>
  );
}
