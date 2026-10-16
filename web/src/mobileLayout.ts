// Phone layout decisions for reviewing a change set (UI plan Phase 3), kept
// pure so they are tested without a DOM.
//
// At 390px a unified diff line of ordinary code is two or three screens wide.
// Two things make it readable there: wrapping long lines (on by default on a
// narrow screen, a toggle everywhere), and not opening every file at once --
// a twelve-file change set is a long scroll before the Approve button.

// NARROW_MAX is the width at and below which the shell is in its phone
// layout -- the same breakpoint the CSS uses for the sticky decision bar.
export const NARROW_MAX = 760;

export function isNarrow(width: number): boolean {
  return width <= NARROW_MAX;
}

// narrowNow reads the current viewport; false where there is no window.
export function narrowNow(): boolean {
  if (typeof window === "undefined") return false;
  if (typeof window.matchMedia === "function") return window.matchMedia(`(max-width: ${NARROW_MAX}px)`).matches;
  return isNarrow(window.innerWidth);
}

// COLLAPSE_AFTER is how many files a phone opens before the rest start
// collapsed: enough to see the change, few enough to reach the decision.
export const COLLAPSE_AFTER = 2;

// initialCollapsed is the set of file paths that start collapsed: none on a
// wide screen or for a short change set, everything past the first
// COLLAPSE_AFTER on a phone.
export function initialCollapsed(paths: string[], narrow: boolean): Set<string> {
  if (!narrow || paths.length <= COLLAPSE_AFTER + 1) return new Set();
  return new Set(paths.slice(COLLAPSE_AFTER));
}

// toggled returns a copy of set with path flipped.
export function toggled(set: Set<string>, path: string): Set<string> {
  const next = new Set(set);
  if (next.has(path)) next.delete(path);
  else next.add(path);
  return next;
}

// diffStat counts added and removed lines in a unified patch, for the
// collapsed file header -- what a reviewer decides whether to open on.
export function diffStat(patch: string): { added: number; removed: number } {
  let added = 0;
  let removed = 0;
  for (const line of patch.split("\n")) {
    if (line.startsWith("+++") || line.startsWith("---")) continue;
    if (line.startsWith("+")) added++;
    else if (line.startsWith("-")) removed++;
  }
  return { added, removed };
}
