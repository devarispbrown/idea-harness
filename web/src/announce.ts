// announce: one polite live region for the whole app (LiveAnnouncer.tsx,
// rendered once by App). Async actions whose result is otherwise only a
// chip changing colour somewhere on the page -- an approve, an apply, a run
// finishing -- say what happened through it, so a screen-reader user hears
// the outcome without hunting for it.
//
// Polite and deliberately quiet: one message per completed action or run
// state change, never per streamed event (the run panel's live action chip
// is NOT a live region -- it changes every second while an agent works).

type Listener = (message: string) => void;

const listeners = new Set<Listener>();

function emit(m: string) {
  for (const l of listeners) l(m);
}

// One thing ending can reach the region from two places at once: an agent
// run's end is both the run panel's news and the running card's ("Executing
// the plan finished."), and a short run -- the demo executor's, a run that
// fails on its first call -- went live and ended inside one poll, so both
// were heard back to back. A message may therefore name a topic (what it is
// about, e.g. "run:<initiative>") and a rank (how specific it is): messages
// on one topic are held for HOLD_MS and only the most specific is said, and
// for SETTLE_MS after that a less specific one on the same topic is dropped
// as a late echo of what was just said. Messages without a topic are said at
// once, as before.
export const HOLD_MS = 800;
export const SETTLE_MS = 4000;

export interface AnnounceOptions {
  topic?: string;
  rank?: number;
}

const held = new Map<string, { message: string; rank: number; timer: ReturnType<typeof setTimeout> }>();
const said = new Map<string, { rank: number; at: number }>();

export function announce(message: string, { topic, rank = 0 }: AnnounceOptions = {}) {
  const m = message.trim();
  if (!m) return;
  if (!topic) {
    emit(m);
    return;
  }
  const last = said.get(topic);
  if (last && Date.now() - last.at < SETTLE_MS && rank < last.rank) return;
  const pending = held.get(topic);
  if (pending) {
    // The later of two equally specific messages is the newer news.
    if (rank < pending.rank) return;
    clearTimeout(pending.timer);
  }
  const timer = setTimeout(() => {
    held.delete(topic);
    said.set(topic, { rank, at: Date.now() });
    emit(m);
  }, HOLD_MS);
  held.set(topic, { message: m, rank, timer });
}

// resetAnnouncements forgets held and recently said topics (tests).
export function resetAnnouncements() {
  for (const h of held.values()) clearTimeout(h.timer);
  held.clear();
  said.clear();
}

// The run topic and its ranks: the operation's end names the operation and
// how it ended, so it is the more specific of the two.
export const runTopic = (initiativeId: string) => `run:${initiativeId}`;
export const RUN_RANK = { runStarted: 1, operationEnded: 2 } as const;

export function onAnnounce(l: Listener): () => void {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

// nextAnnouncement is the text the region shows for message after prev.
// Screen readers announce a live region only when its text changes, so the
// same message twice in a row (approving two change sets) would be heard
// once; a trailing no-break space alternates to make the second one new.
export function nextAnnouncement(prev: string, message: string): string {
  if (prev === message) return message + " ";
  return message;
}

// runStatusAnnouncement is what the run panel says when a run goes live:
// once per run, "" otherwise -- a run that was already over when the panel
// first saw it is history, not news. The end is the running card's to say
// (operationAnnouncement): the card, and the panel inside it, leave as the
// operation ends, often before the run stream's own end arrives, so a panel
// that waited for it would miss it.
export function runStatusAnnouncement(
  prev: { runId: string; live: boolean } | null,
  next: { runId: string; live: boolean },
): string {
  if (!next.runId || !next.live) return "";
  if (!prev || prev.runId !== next.runId || !prev.live) return "Agent run started.";
  return "";
}

// operationAnnouncement is what the running card says as an operation comes
// and goes. An agent run's start is the run panel's to say (with the run
// itself), so execute is silent on start; every operation's end is said
// here, since the card -- and the panel inside it -- leave with it.
export function operationAnnouncement(
  prev: { id: string; type: string } | null,
  next: { id: string; type: string } | null,
  label: (type: string) => string,
  stopped: boolean,
): string {
  if (next && prev?.id !== next.id) return next.type === "execute_initiative" ? "" : `${label(next.type)} started.`;
  if (!next && prev) return `${label(prev.type)} ${stopped ? "stopped" : "finished"}.`;
  return "";
}
