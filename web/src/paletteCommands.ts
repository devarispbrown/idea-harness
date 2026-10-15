// paletteCommands: what the ⌘K palette (CommandPalette.tsx) offers, as data,
// so the list and its matching are unit-tested without rendering. Each
// command is a destination: a route, optionally a section heading on it to
// land focus on (Settings › Executor).

import { PHASES, phaseHref, type PhaseName } from "./router";
import { PHASE_LABELS } from "./phases";
import { lineageHref } from "./lineage";

export interface PaletteCommand {
  id: string;
  label: string;
  hint: string;
  href: string;
  // section is an h2's text on the destination page; focus lands there.
  section?: string;
  // keywords match too, so "timeline" finds Activity and "deps" nothing
  // surprising.
  keywords?: string[];
}

// The Settings page's sections, by their h2. Matched by text, so a section
// that moves or is split out into its own component is still found; one
// that is renamed just lands on the page heading instead.
export const SETTINGS_SECTIONS = [
  "Model",
  "Providers",
  "Add provider",
  "Executor",
  "Git",
  "Review rigor",
  "Secret exemptions",
  "Integrations",
];

// A node code -> its lineage type. The codes the daemon assigns and the
// lineage route resolves (docs/ui.md, Product Graph views).
const CODE_TYPES: Record<string, string> = {
  REQ: "requirement",
  TASK: "task",
  OBJ: "objective",
  MET: "metric",
  DEC: "decision",
  CHG: "change_set",
};

// nodeCodeCommand turns a typed code (REQ-3, task-2, OBJ-1, MET-1, DEC-4,
// CHG-1) into "open its lineage", or null when the query is not a code.
// Needs an open initiative: codes are per initiative.
export function nodeCodeCommand(initiativeId: string, query: string): PaletteCommand | null {
  if (!initiativeId) return null;
  const m = /^\s*([a-z]+)-(\d+)\s*$/i.exec(query);
  if (!m) return null;
  const prefix = m[1].toUpperCase();
  const type = CODE_TYPES[prefix];
  if (!type) return null;
  const code = `${prefix}-${Number(m[2])}`;
  return {
    id: `lineage-${code}`,
    label: `Lineage of ${code}`,
    hint: "lineage",
    href: lineageHref(initiativeId, type, code),
  };
}

export function paletteCommands(initiativeId: string): PaletteCommand[] {
  const out: PaletteCommand[] = [];
  if (initiativeId) {
    for (const p of PHASES) {
      const phase = p as PhaseName;
      out.push({ id: `phase-${phase}`, label: PHASE_LABELS[phase], hint: "phase", href: phaseHref(initiativeId, phase) });
    }
    const at = (path: string) => `#/i/${initiativeId}/${path}`;
    out.push(
      { id: "gaps", label: "Gaps", hint: "product graph", href: at("gaps"), keywords: ["missing", "graph"] },
      { id: "outcomes", label: "Outcomes", hint: "product graph", href: at("outcomes"), keywords: ["objectives", "metrics", "drift"] },
      { id: "quality", label: "Quality", hint: "product graph", href: at("quality"), keywords: ["scorecard", "dimensions"] },
      { id: "activity", label: "Activity", hint: "what changed", href: at("changed"), keywords: ["timeline", "history", "what changed"] },
      { id: "health", label: "Graph health", hint: "health", href: at("health"), keywords: ["risk", "doctor"] },
    );
  }
  out.push(
    { id: "home", label: "All initiatives", hint: "home", href: "#/" },
    { id: "queue", label: "Needs you", hint: "queue", href: "#/queue", keywords: ["queue", "inbox", "attention"] },
    { id: "settings", label: "Settings", hint: "settings", href: "#/settings" },
  );
  for (const s of SETTINGS_SECTIONS) {
    out.push({ id: `settings-${s}`, label: `Settings › ${s}`, hint: "settings", href: "#/settings", section: s });
  }
  return out;
}

// filterCommands keeps the commands whose label or a keyword contains the
// query (case-insensitive), with a typed node code's lineage first.
export function filterCommands(commands: PaletteCommand[], initiativeId: string, query: string): PaletteCommand[] {
  const q = query.trim().toLowerCase();
  const code = nodeCodeCommand(initiativeId, query);
  const matched = !q
    ? commands
    : commands.filter((c) => c.label.toLowerCase().includes(q) || (c.keywords ?? []).some((k) => k.includes(q)));
  return code ? [code, ...matched] : matched;
}
