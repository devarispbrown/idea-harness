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

export function announce(message: string) {
  const m = message.trim();
  if (!m) return;
  for (const l of listeners) l(m);
}

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
