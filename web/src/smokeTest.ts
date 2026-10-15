// Pure logic for Settings → Executor's "Run smoke test" button (S3):
// whether it can run right now, and the copy for its running/result/
// last-run states. Extracted for the same reason executorStatus.ts and
// buildLogic.ts are: none of this needs a DOM or a fetch mock to test.

// smokeButtonState mirrors the daemon's own refusal rules for
// POST .../executors/smoke (400 for a non-agent kind, or a resolvable
// provider/model problem, which the daemon reports here as a non-empty
// `problem`): only hermes/omp actually run the coding agent, and only when
// the rest of the executor's status is otherwise clean. `status` is
// undefined before the executors list has loaded at all -- disabled with no
// reason yet, rather than a misleading "select hermes or omp" while the
// real answer is still in flight.
export interface SmokeButtonState {
  enabled: boolean;
  // Why the button is disabled; empty when it's enabled.
  reason: string;
}

export function smokeButtonState(
  kind: string,
  status: { problem: string; last_smoke?: { status: string } } | undefined,
): SmokeButtonState {
  // A smoke already running (started in this tab or another, or before a
  // reload) makes a click a 409; say so instead.
  if (status?.last_smoke?.status === "running" || status?.last_smoke?.status === "pending") {
    return { enabled: false, reason: "A smoke test is running." };
  }
  // The daemon smokes every coding-agent kind (POST .../executors/smoke):
  // hermes, omp, acp and openhands. Offering only the first two left the
  // ACP/OpenHands profiles the Settings editor now configures with no way to
  // prove them from the UI.
  if (kind !== "hermes" && kind !== "omp" && kind !== "acp" && kind !== "openhands") {
    return {
      enabled: false,
      reason: "The smoke test runs the coding agent; select a coding-agent executor (hermes, omp, acp or openhands) first.",
    };
  }
  if (status === undefined) {
    return { enabled: false, reason: "" };
  }
  if (status.problem) {
    return { enabled: false, reason: status.problem };
  }
  return { enabled: true, reason: "" };
}

export interface LastSmoke {
  status: string;
  finished_at?: string;
  error?: string;
}

// relativeTime turns a past ISO timestamp into "just now" / "5 min ago" /
// "2 hrs ago" / "3 days ago". `now` (ms epoch) is passed in rather than read
// via Date.now() internally so this stays pure and testable without faking
// the clock. Invalid input (a malformed or missing timestamp) is the empty
// string, not "NaN min ago" or a thrown exception.
export function relativeTime(iso: string, now: number): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "";
  const seconds = Math.max(0, Math.round((now - then) / 1000));
  if (seconds < 60) return "just now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} hr${hours === 1 ? "" : "s"} ago`;
  const days = Math.round(hours / 24);
  return `${days} day${days === 1 ? "" : "s"} ago`;
}

// lastSmokeMessage is the Settings page's "Last smoke test ..." line, read
// from the daemon's `status.last_smoke` (S2) on load -- so the result
// survives a reload even though the browser has no idea which operation id,
// if any, it was polling before. Empty when nothing has run yet.
export function lastSmokeMessage(last: LastSmoke | undefined, now: number): string {
  if (!last) return "";
  // A smoke test can still be mid-flight in the daemon's own record (e.g. a
  // reload landed while one from another tab/session is running) -- that's
  // not "last smoke passed/failed", it's still going.
  if (last.status === "running" || last.status === "pending") {
    return "A smoke test is running.";
  }
  const when = last.finished_at ? relativeTime(last.finished_at, now) : "";
  const verb =
    last.status === "succeeded" ? "passed" : last.status === "cancelled" ? "was cancelled" : "failed";
  return when ? `Last smoke test ${verb} ${when}.` : `Last smoke test ${verb}.`;
}

// formatSmokeElapsed is the running-state elapsed timer, e.g. "12s" / "1m 4s".
export function formatSmokeElapsed(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const m = Math.floor(s / 60);
  const sec = s % 60;
  return m > 0 ? `${m}m ${sec}s` : `${sec}s`;
}
