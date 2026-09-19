// Cost and time formatting (C3): "Cost was invisible." pure formatting so the
// compact spend-and-time line, and the per-phase breakdown behind it, are
// unit-testable without a DOM. Consumed by PhaseNav's CostLine.
//
// Wire contract (wave 3, GET .../initiatives/{id}/costs):
//   { total_usd, by_phase: [{phase, usd, seconds, calls}],
//     agent: { runs, seconds, usd: number|null, note } }
// This is backend work landing alongside this UI -- every field here is
// read with optional chaining at the call site, and every formatter below
// accepts `undefined` (field absent: an old daemon, or the endpoint not
// deployed yet) as well as `null` (field present but genuinely unmeasured).

export interface CostByPhase {
  phase: string;
  usd: number;
  seconds: number;
  calls: number;
}

export interface AgentCost {
  runs: number;
  seconds: number;
  // null: the daemon could not estimate agent spend from the executor's
  // usage output for this run (e.g. an executor that reports no usage).
  usd: number | null;
  note?: string;
}

export interface CostsView {
  total_usd: number;
  by_phase: CostByPhase[];
  agent: AgentCost;
}

// formatUsd renders a dollar amount, or "not measured" for a `null` that
// means "we tried and couldn't", never for merely absent/undefined data
// (the call site omits the whole line in that case instead).
export function formatUsd(usd: number | null): string {
  if (usd === null) return "not measured";
  if (!Number.isFinite(usd)) return "not measured";
  // Sub-cent spend (a handful of tokens) would round to "$0.00", which reads
  // as free rather than merely small -- three decimals below a cent, two
  // decimals at or above it.
  const digits = usd > 0 && usd < 0.01 ? 3 : 2;
  return `$${usd.toFixed(digits)}`;
}

// formatDuration renders a whole number of seconds as a short human string:
// "45s", "3m 12s", "1h 05m". Never blank, even for 0.
export function formatDuration(totalSeconds: number): string {
  const seconds = Math.max(0, Math.floor(totalSeconds || 0));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remSeconds = seconds % 60;
  if (minutes < 60) {
    return remSeconds > 0 ? `${minutes}m ${remSeconds}s` : `${minutes}m`;
  }
  const hours = Math.floor(minutes / 60);
  const remMinutes = minutes % 60;
  return `${hours}h ${String(remMinutes).padStart(2, "0")}m`;
}

// totalSeconds sums the per-phase durations -- the costs endpoint has no
// single "total time" field of its own, only totals per phase plus the
// agent's own separately-tracked seconds.
export function totalPhaseSeconds(byPhase: CostByPhase[]): number {
  return byPhase.reduce((sum, p) => sum + (p.seconds || 0), 0);
}

// costLine is the compact header/home line: "$1.42 · 12m · agent: not
// measured". Omits the agent segment entirely when there is no agent
// activity yet (0 runs) rather than printing "agent: not measured" for an
// initiative that simply hasn't executed anything.
export function costLine(costs: CostsView): string {
  const money = formatUsd(costs.total_usd ?? null);
  const time = formatDuration(totalPhaseSeconds(costs.by_phase ?? []));
  const parts = [money, time];
  if (costs.agent && costs.agent.runs > 0) {
    parts.push(`agent: ${formatUsd(costs.agent.usd)}`);
  }
  return parts.join(" · ");
}

// phaseBreakdownLines is the per-phase detail shown on click, one line per
// phase that actually spent something (a phase with 0 calls and 0 seconds
// is not worth a row).
export function phaseBreakdownLines(byPhase: CostByPhase[]): string[] {
  return (byPhase ?? [])
    .filter((p) => p.calls > 0 || p.seconds > 0 || p.usd > 0)
    .map((p) => `${p.phase}: ${formatUsd(p.usd)} · ${formatDuration(p.seconds)} · ${p.calls} call${p.calls === 1 ? "" : "s"}`);
}
