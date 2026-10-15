// Keyboard shortcuts: the one list the `?` reference (ShortcutsDialog.tsx)
// renders and docs/ui.md documents, plus the small predicates the handlers
// share. A shortcut added anywhere belongs in SHORTCUTS, so the reference
// cannot fall behind what the keyboard actually does.

// isMac picks the modifier the reference names: ⌘ on a Mac, Ctrl elsewhere.
// Both work everywhere (handlers accept metaKey || ctrlKey); this is only
// what is printed.
export function isMac(platform = typeof navigator !== "undefined" ? navigator.platform || navigator.userAgent : ""): boolean {
  return /Mac|iPhone|iPad|iPod/i.test(platform);
}

export function modLabel(mac = isMac()): string {
  return mac ? "⌘" : "Ctrl";
}

interface KeyLike {
  key: string;
  metaKey?: boolean;
  ctrlKey?: boolean;
  altKey?: boolean;
  shiftKey?: boolean;
  isComposing?: boolean;
}

// isSubmitShortcut is ⌘+Enter / Ctrl+Enter: send the focused note or form.
// Not while an IME is composing (Enter there commits the composition).
export function isSubmitShortcut(e: KeyLike): boolean {
  return e.key === "Enter" && (!!e.metaKey || !!e.ctrlKey) && !e.altKey && !e.isComposing;
}

// isEditableTarget reports whether a keystroke is typing, so a bare-key
// shortcut (`?`) never fires while someone types a question mark into a
// field.
export function isEditableTarget(t: EventTarget | null): boolean {
  if (!t || typeof (t as HTMLElement).tagName !== "string") return false;
  const el = t as HTMLElement;
  const tag = el.tagName.toLowerCase();
  if (tag === "textarea" || tag === "select") return true;
  if (tag === "input") {
    const type = ((el as HTMLInputElement).type || "text").toLowerCase();
    return !["button", "submit", "reset", "checkbox", "radio", "range", "color", "file", "image"].includes(type);
  }
  return el.isContentEditable;
}

// isHelpShortcut is `?` outside a text field, with no modifier but Shift
// (which is how `?` is typed on most layouts).
export function isHelpShortcut(e: KeyLike & { target: EventTarget | null }): boolean {
  return e.key === "?" && !e.metaKey && !e.ctrlKey && !e.altKey && !isEditableTarget(e.target);
}

export interface Shortcut {
  keys: string[];
  does: string;
}

export interface ShortcutGroup {
  title: string;
  items: Shortcut[];
}

export function shortcutGroups(mac = isMac()): ShortcutGroup[] {
  const mod = modLabel(mac);
  return [
    {
      title: "Anywhere",
      items: [
        { keys: [mod, "K"], does: "Open the command palette: phases, Gaps, Outcomes, Quality, Activity, Needs you, Settings sections, and any node code (REQ-3, TASK-2, OBJ-1, MET-1) to open its lineage" },
        { keys: ["?"], does: "Show these shortcuts (not while typing in a field)" },
        { keys: ["Tab"], does: "From the top of the page, the first stop is Skip to content" },
      ],
    },
    {
      title: "In a note or form",
      items: [
        { keys: [mod, "Enter"], does: "Send it: the run note, Request changes, a plan revision, a retry, a note to the running agent, an answer or your own resolution" },
        { keys: ["Esc"], does: "Close the open note form or dialog and return to the button that opened it" },
      ],
    },
    {
      title: "Command palette",
      items: [
        { keys: ["↑", "↓"], does: "Move through the matches" },
        { keys: ["Enter"], does: "Go there" },
        { keys: ["Esc"], does: "Close it" },
      ],
    },
  ];
}
