// Pure logic for the spending-cap meter (GET .../initiatives/{id}/budget),
// shown in the initiative shell and on Build, and the cap inputs that set it.

export interface BudgetStatus {
  cap_usd: number | null;
  source: "initiative" | "project" | "none";
  initiative_cap_usd: number | null;
  project_default_usd: number;
  spent_usd: number;
  model_usd: number;
  agent_usd: number | null;
  agent_runs: number;
  agent_not_counted: boolean;
  remaining_usd: number | null;
  exceeded: boolean;
  parked: boolean;
  note: string;
}

export type MeterTone = "ok" | "warn" | "over";

function usd(n: number): string {
  // Cents once there is something to see; four places below a cent, so a
  // demo's $0.0010 calls are not all "$0.00".
  return n !== 0 && Math.abs(n) < 0.01 ? `$${n.toFixed(4)}` : `$${n.toFixed(2)}`;
}

export function formatUSD(n: number): string {
  return usd(n);
}

// meterFraction is how full the meter is, 0..1 (1 at or over the cap).
export function meterFraction(b: BudgetStatus): number {
  if (b.cap_usd === null || b.cap_usd <= 0) return 0;
  return Math.max(0, Math.min(1, b.spent_usd / b.cap_usd));
}

export function meterTone(b: BudgetStatus): MeterTone {
  if (b.exceeded || b.parked) return "over";
  return meterFraction(b) >= 0.8 ? "warn" : "ok";
}

// meterLabel is the one line beside the bar.
export function meterLabel(b: BudgetStatus): string {
  if (b.cap_usd === null) return `${usd(b.spent_usd)} spent · no cap`;
  const src = b.source === "initiative" ? "this initiative's cap" : "project default";
  return `${usd(b.spent_usd)} of ${usd(b.cap_usd)} (${src})`;
}

// agentCaveat says, every time, when the meter does not include the agent.
export function agentCaveat(b: BudgetStatus): string {
  if (b.agent_not_counted) {
    return `The coding agent ran ${b.agent_runs} time(s) without reporting a cost (hermes and omp usually do not), so its spend is not counted here.`;
  }
  return "";
}

// showInShell decides whether the compact shell meter appears at all: only
// when there is a cap to measure against, or the initiative is stopped at
// one. An uncapped initiative already shows its spend in the sidebar.
export function showInShell(b: BudgetStatus | null): boolean {
  return !!b && (b.cap_usd !== null || b.parked);
}

// capInputError checks a typed cap. allowZero is the project default, where
// 0 means "no cap"; an initiative's own cap must be > 0 (Clear returns it to
// the default instead).
export function capInputError(text: string, allowZero: boolean): string {
  const t = text.trim().replace(/^\$/, "");
  if (t === "") return "Enter a dollar amount.";
  const n = Number(t);
  if (!Number.isFinite(n) || n < 0) return "A dollar amount, e.g. 5 or 2.50.";
  if (!allowZero && n === 0) return "Must be more than $0 — use Clear to go back to the project default.";
  return "";
}

export function parseCap(text: string): number {
  return Number(text.trim().replace(/^\$/, ""));
}

// raiseHint suggests a cap that would let the stopped initiative go on:
// the spend so far plus the old cap again, rounded up to the dollar.
export function raiseHint(b: BudgetStatus): number {
  const base = b.cap_usd ?? 0;
  return Math.max(1, Math.ceil(b.spent_usd + Math.max(base, 1)));
}
