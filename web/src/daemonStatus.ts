// Pure daemon-down decision logic (H1), kept separate from the fetch calls so
// it is testable without mocking fetch/timers.
//
// Before this, the recurring 2.5s poll never flipped `daemonDown` at all --
// only the one-shot initial project load did -- and each poller that DID fail
// wrote `[]` straight into state on its own `.catch(() => [])`. When the
// daemon died mid-session, every subsequent poll silently overwrote artifacts
// / tasks / change sets with empty arrays, which is what rewrote the rail to
// "plan approved", "PRD v0 approved" and "0 tasks approved": derivePhases saw
// empty inputs and recomputed the summaries from scratch. The fix has two
// halves: this module decides WHETHER a round of polling means the daemon is
// down, and the callers in App.tsx only ever apply a poll's result on
// success, so a failure can flip `daemonDown` but never clobbers the last
// good data.

export interface PollOutcome {
  ok: boolean;
}

// A round of polling (one or more requests fired together) marks the daemon
// down if any of them failed. An empty round (nothing was polled) is not
// evidence of anything, so it reports "up".
export function daemonDownAfterPoll(results: PollOutcome[]): boolean {
  return results.length > 0 && results.some((r) => !r.ok);
}

// The lost-connection message used to hard-code 127.0.0.1:8787, which is
// wrong whenever the UI is reached through a different host or port (a
// tunnel, a non-default --addr, a reverse proxy). `host` should be
// `location.host` at the call site.
export function daemonLostMessage(host: string): string {
  return `Lost connection to the daemon at ${host}.`;
}
