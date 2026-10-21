import { createPortal } from "react-dom";
import { useRef } from "react";
import { useDialog } from "./focus";
import { GLOSSARY } from "./glossary";

// GlossaryDialog is the domain-term reference (P1.2). A modal dialog in the
// same style as ShortcutsDialog: focus moves in, Tab stays in, Escape or
// Close returns focus where it was.
export default function GlossaryDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const panelRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);

  useDialog(panelRef, open, onClose, closeRef);

  if (!open) return null;
  return createPortal(
    <div className="cmd-overlay" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div
        ref={panelRef}
        className="cmd-panel shortcuts-panel"
        role="dialog"
        aria-modal="true"
        aria-labelledby="glossary-title"
      >
        <div className="shortcuts-head">
          <h2 id="glossary-title" className="shortcuts-title">
            Glossary
          </h2>
          <button ref={closeRef} className="ghost" onClick={onClose}>
            Close
          </button>
        </div>
        <div className="shortcuts-body">
          <dl className="shortcuts-list">
            {GLOSSARY.map((g) => (
              <div key={g.term} className="glossary-row">
                <dt className="glossary-term">{g.term}</dt>
                <dd className="glossary-def">{g.def}</dd>
              </div>
            ))}
          </dl>
        </div>
      </div>
    </div>,
    document.body,
  );
}
