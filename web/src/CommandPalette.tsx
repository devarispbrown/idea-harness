import { createPortal } from "react-dom";
import { useEffect, useMemo, useRef, useState } from "react";
import { filterCommands, paletteCommands, type PaletteCommand } from "./paletteCommands";
import { dialogRoot, focusWhenReady, sectionHeading, setPendingFocus, useDialog } from "./focus";

// CommandPalette is the ⌘K / Ctrl-K quick navigation overlay. It uses the WAI
// combobox pattern (single input with aria-activedescendant over a listbox of
// options), driven entirely from the keyboard: ↑/↓ move, Enter runs, Escape
// closes, and Tab is trapped within the palette (useDialog, which also gives
// focus back to where it was on close).
//
// What it offers is paletteCommands.ts: the phases, the Product Graph views,
// Activity, Needs you, Settings and each of its sections, and -- for a typed
// node code (REQ-3, TASK-2, OBJ-1, MET-1) -- that node's lineage.
export default function CommandPalette({
  initiativeId,
  onNavigate,
}: {
  initiativeId: string;
  onNavigate: (href: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [sel, setSel] = useState(0);
  const panelRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const commands = useMemo(() => paletteCommands(initiativeId), [initiativeId]);
  const filtered = useMemo(() => filterCommands(commands, initiativeId, query), [commands, initiativeId, query]);

  // Global toggle (Cmd/Ctrl-K). Escape and Tab are useDialog's.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((v) => !v);
        setQuery("");
        setSel(0);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useDialog(panelRef, open, () => setOpen(false), inputRef);

  // Keep the selection in bounds as the filtered list shrinks.
  useEffect(() => {
    if (sel >= filtered.length) setSel(0);
  }, [filtered.length, sel]);

  if (!open) return null;

  const run = (c: PaletteCommand) => {
    setOpen(false);
    if (c.section) {
      const section = c.section;
      const find = () => sectionHeading(section);
      if (window.location.hash === c.href) {
        // Already on the page: no route change will come to focus it.
        // After the dialog's own focus restore, which runs on close.
        setTimeout(() => focusWhenReady(find), 0);
      } else {
        setPendingFocus(find);
      }
    }
    onNavigate(c.href);
  };

  // Portaled into <body>, outside the app root, so useDialog can make the
  // app root inert while this is open (focus.ts, inertOutside).
  return createPortal(
    <div
      className="cmd-overlay"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) setOpen(false);
      }}
    >
      <div ref={panelRef} className="cmd-panel" role="dialog" aria-modal="true" aria-label="Command palette">
        <input
          ref={inputRef}
          className="cmd-input"
          placeholder="Jump to… (a page, a setting, or a code like REQ-3)"
          aria-label="Jump to a page, a setting, or a node code"
          role="combobox"
          aria-expanded="true"
          aria-controls="cmd-list"
          aria-autocomplete="list"
          aria-activedescendant={filtered[sel] ? `cmd-opt-${sel}` : undefined}
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setSel(0);
          }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setSel((s) => (filtered.length ? (s + 1) % filtered.length : 0));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setSel((s) => (filtered.length ? (s - 1 + filtered.length) % filtered.length : 0));
            } else if (e.key === "Enter") {
              e.preventDefault();
              if (filtered[sel]) run(filtered[sel]);
            }
          }}
        />
        {filtered.length === 0 ? (
          <div className="cmd-empty" role="status">
            No matches.
          </div>
        ) : (
          <ul className="cmd-list" id="cmd-list" role="listbox" aria-label="Commands">
            {filtered.map((c, i) => (
              <li
                key={c.id}
                id={`cmd-opt-${i}`}
                role="option"
                aria-selected={i === sel}
                className={`cmd-item ${i === sel ? "sel" : ""}`}
                onMouseEnter={() => setSel(i)}
                onClick={() => run(c)}
              >
                <span className="cmd-label">{c.label}</span>
                {c.hint && <span className="cmd-hint">{c.hint}</span>}
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>,
    dialogRoot(),
  );
}
