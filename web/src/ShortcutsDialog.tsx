import { useEffect, useRef, useState } from "react";
import { useDialog } from "./focus";
import { isHelpShortcut, shortcutGroups } from "./shortcuts";

// ShortcutsDialog is the `?` reference: every keyboard shortcut, from the
// same list docs/ui.md documents (shortcuts.ts). A modal dialog -- focus
// moves in, Tab stays in, Escape or Close returns focus where it was.
export default function ShortcutsDialog() {
  const [open, setOpen] = useState(false);
  const panelRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!open && isHelpShortcut(e)) {
        e.preventDefault();
        setOpen(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  useDialog(panelRef, open, () => setOpen(false), closeRef);

  if (!open) return null;
  const groups = shortcutGroups();
  return (
    <div
      className="cmd-overlay"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) setOpen(false);
      }}
    >
      <div
        ref={panelRef}
        className="cmd-panel shortcuts-panel"
        role="dialog"
        aria-modal="true"
        aria-labelledby="shortcuts-title"
      >
        <div className="shortcuts-head">
          <h2 id="shortcuts-title" className="shortcuts-title">
            Keyboard shortcuts
          </h2>
          <button ref={closeRef} className="ghost" onClick={() => setOpen(false)}>
            Close
          </button>
        </div>
        <div className="shortcuts-body">
          {groups.map((g) => (
            <section key={g.title} className="shortcuts-group" aria-labelledby={`sc-${g.title}`}>
              <h3 id={`sc-${g.title}`} className="shortcuts-group-title">
                {g.title}
              </h3>
              <dl className="shortcuts-list">
                {g.items.map((s) => (
                  <div key={s.keys.join("+") + s.does} className="shortcuts-row">
                    <dt>
                      {s.keys.map((k, i) => (
                        <span key={i}>
                          {i > 0 && <span aria-hidden="true"> </span>}
                          <kbd>{k}</kbd>
                        </span>
                      ))}
                    </dt>
                    <dd>{s.does}</dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </div>
    </div>
  );
}
