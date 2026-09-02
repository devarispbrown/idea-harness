import { useEffect, useMemo, useRef, useState } from "react";
import { PHASES, phaseHref } from "./router";
import type { PhaseName } from "./router";
import { PHASE_LABELS } from "./phases";

interface Cmd {
  id: string;
  label: string;
  hint?: string;
  run: () => void;
}

// CommandPalette is the ⌘K / Ctrl-K quick navigation overlay. It uses the WAI
// combobox pattern (single input with aria-activedescendant over a listbox of
// options), driven entirely from the keyboard: ↑/↓ move, Enter runs, Escape
// closes, and Tab is trapped within the palette.
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
  const inputRef = useRef<HTMLInputElement>(null);
  const prevFocusRef = useRef<HTMLElement | null>(null);

  const commands = useMemo<Cmd[]>(() => {
    const out: Cmd[] = [];
    if (initiativeId) {
      for (const p of PHASES) {
        const phase = p as PhaseName;
        out.push({
          id: `phase-${phase}`,
          label: PHASE_LABELS[phase],
          hint: "phase",
          run: () => onNavigate(phaseHref(initiativeId, phase)),
        });
      }
    }
    out.push({ id: "home", label: "All initiatives", hint: "home", run: () => onNavigate("#/") });
    out.push({ id: "settings", label: "Settings", hint: "settings", run: () => onNavigate("#/settings") });
    return out;
  }, [initiativeId, onNavigate]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return commands;
    return commands.filter((c) => c.label.toLowerCase().includes(q));
  }, [commands, query]);

  // Global toggle (Cmd/Ctrl-K) + Escape-to-close.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((v) => !v);
        setQuery("");
        setSel(0);
      } else if (e.key === "Escape" && open) {
        setOpen(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  // Remember prior focus on open; restore it on close.
  useEffect(() => {
    if (open) {
      prevFocusRef.current = document.activeElement as HTMLElement | null;
      setQuery("");
      setSel(0);
      requestAnimationFrame(() => inputRef.current?.focus());
    } else {
      prevFocusRef.current?.focus?.();
      prevFocusRef.current = null;
    }
  }, [open]);

  // Keep the selection in bounds as the filtered list shrinks.
  useEffect(() => {
    if (sel >= filtered.length) setSel(0);
  }, [filtered.length, sel]);

  if (!open) return null;

  const run = (c: Cmd) => {
    setOpen(false);
    c.run();
  };

  return (
    <div
      className="cmd-overlay"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) setOpen(false);
      }}
    >
      <div className="cmd-panel" role="dialog" aria-modal="true" aria-label="Command palette">
        <input
          ref={inputRef}
          className="cmd-input"
          placeholder="Jump to…"
          role="combobox"
          aria-expanded="true"
          aria-controls="cmd-list"
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
            } else if (e.key === "Tab") {
              e.preventDefault(); // trap focus inside the modal palette
            }
          }}
        />
        {filtered.length === 0 ? (
          <div className="cmd-empty">No matches.</div>
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
    </div>
  );
}
