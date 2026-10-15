import { useEffect, useRef, type RefObject } from "react";

// Focus management shared by the shell and every dialog (docs/ui.md,
// "Keyboard and accessibility"): where focus goes on a route change, how a
// modal keeps Tab inside itself and gives focus back when it closes, and how
// a command that lands on a section of a page puts focus there.

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])';

// focusableWithin lists the elements Tab can reach inside root, in order.
// Hidden ones (display: none, or inside a closed <details>) are left out by
// checking for a layout box -- jsdom has none, so there everything counts.
export function focusableWithin(root: HTMLElement): HTMLElement[] {
  const all = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE));
  const hasLayout = typeof navigator !== "undefined" && !/jsdom/i.test(navigator.userAgent);
  return all.filter((el) => !hasLayout || el.getClientRects().length > 0);
}

// trapTab keeps Tab and Shift+Tab cycling inside root. Returns whether it
// handled the key.
export function trapTab(e: KeyboardEvent | React.KeyboardEvent, root: HTMLElement | null): boolean {
  if (e.key !== "Tab" || !root) return false;
  const items = focusableWithin(root);
  if (items.length === 0) {
    e.preventDefault();
    return true;
  }
  const first = items[0];
  const last = items[items.length - 1];
  const active = document.activeElement as HTMLElement | null;
  const inside = !!active && root.contains(active);
  if (e.shiftKey && (active === first || !inside)) {
    e.preventDefault();
    last.focus();
    return true;
  }
  if (!e.shiftKey && (active === last || !inside)) {
    e.preventDefault();
    first.focus();
    return true;
  }
  return false;
}

// useDialog is the modal contract every dialog here keeps: on open it
// remembers what had focus and focuses the dialog's first control (or
// `initial`); while open, Tab stays inside and Escape calls onClose; on close
// focus goes back where it was, so a keyboard user is not dropped at the top
// of the page.
//
// modal: false is the same contract for an inline disclosure (a note form
// that opens under its button, the revert confirmation): focus moves in and
// comes back, Escape closes it -- but only while focus is inside it, and Tab
// is not trapped, because the rest of the page is still there to use.
export function useDialog(
  ref: RefObject<HTMLElement>,
  open: boolean,
  onClose: () => void,
  initial?: RefObject<HTMLElement>,
  { modal = true }: { modal?: boolean } = {},
) {
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    // Synchronously, in the effect after the dialog is in the DOM: a key
    // typed the instant ⌘K opens the palette must land in its input, not in
    // whatever had focus a frame earlier.
    const first = initial?.current ?? (ref.current ? focusableWithin(ref.current)[0] : null) ?? ref.current;
    first?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeRef.current();
      } else if (modal) {
        trapTab(e, ref.current);
      }
    };
    // A modal hears every key (capture, so nothing under it acts first); an
    // inline disclosure only the keys typed inside it.
    const target: HTMLElement | Document | null = modal ? document : ref.current;
    target?.addEventListener("keydown", onKey as EventListener, modal);
    return () => {
      target?.removeEventListener("keydown", onKey as EventListener, modal);
      // Only when the element is still on the page: a dialog opened from a
      // control that has since gone (a row that was removed) has nowhere to
      // return to, and focusing a detached node does nothing anyway.
      if (previous && previous.isConnected) previous.focus();
    };
    // ref and initial are refs: stable, read when the dialog opens.
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps
}

// pageHeading is what a route change focuses: the page's h1, else its first
// h2, else the main region itself.
export function pageHeading(main: HTMLElement): HTMLElement {
  return main.querySelector<HTMLElement>("h1") ?? main.querySelector<HTMLElement>("h2") ?? main;
}

// focusTarget focuses el, making it programmatically focusable first when it
// is not (a heading), without adding it to the Tab order.
export function focusTarget(el: HTMLElement) {
  if (!el.hasAttribute("tabindex") && !el.matches(FOCUSABLE)) el.setAttribute("tabindex", "-1");
  el.focus({ preventScroll: false });
}

// focusWhenReady polls for find() (pages render their heading after their
// first fetch, not on mount) and focuses what it returns; after timeoutMs it
// focuses fallback() instead, when given. Returns a cancel function.
export function focusWhenReady(
  find: () => HTMLElement | null,
  timeoutMs = 2000,
  everyMs = 50,
  fallback?: () => HTMLElement | null,
): () => void {
  let cancelled = false;
  const started = Date.now();
  const tick = () => {
    if (cancelled) return;
    const el = find();
    if (el) {
      focusTarget(el);
      return;
    }
    if (Date.now() - started < timeoutMs) {
      timer = setTimeout(tick, everyMs);
      return;
    }
    const fb = fallback?.();
    if (fb) focusTarget(fb);
  };
  let timer = setTimeout(tick, 0);
  return () => {
    cancelled = true;
    clearTimeout(timer);
  };
}

// A command that opens a page AND a place on it (the palette's "Settings ›
// Executor") leaves its target here; the route change focuses it instead of
// the page heading.
let pendingTarget: (() => HTMLElement | null) | null = null;

export function setPendingFocus(find: () => HTMLElement | null) {
  pendingTarget = find;
}

export function takePendingFocus(): (() => HTMLElement | null) | null {
  const t = pendingTarget;
  pendingTarget = null;
  return t;
}

// sectionHeading finds the h2 whose text is name inside main: how the
// palette lands on a Settings section without each section needing an id.
export function sectionHeading(name: string): HTMLElement | null {
  const main = document.getElementById("main-content");
  if (!main) return null;
  const want = name.trim().toLowerCase();
  return (
    Array.from(main.querySelectorAll<HTMLElement>("h2")).find((h) => (h.textContent ?? "").trim().toLowerCase() === want) ?? null
  );
}

// useRouteFocus moves focus to the new page's heading after every route
// change (not on first load: the page has not been navigated to, and
// stealing focus there would skip the skip link). Without it, a keyboard or
// screen-reader user who activated a link stayed on that link -- now
// possibly gone -- with no announcement that the page changed.
export function useRouteFocus(routeKey: string, main: RefObject<HTMLElement>) {
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    const pending = takePendingFocus();
    if (!main.current) return;
    return focusWhenReady(
      pending ?? (() => (main.current ? headingIfRendered(main.current) : null)),
      2000,
      50,
      () => (main.current ? pageHeading(main.current) : null),
    );
  }, [routeKey, main]);
}

// headingIfRendered is pageHeading, but null while the page has no heading
// yet, so focusWhenReady keeps waiting rather than settling on <main> (its
// fallback once the wait is over).
function headingIfRendered(main: HTMLElement): HTMLElement | null {
  return main.querySelector<HTMLElement>("h1") ?? main.querySelector<HTMLElement>("h2");
}

// prefersReducedMotion reports the user's reduced-motion setting, for the
// few places that animate from script (scrollIntoView).
export function prefersReducedMotion(): boolean {
  return typeof window !== "undefined" && !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
}
