// Pure logic for the Automations view (#/automations, GET/PUT
// /projects/{projectId}/automations): run lines, times, and the eval
// settings form.

export interface AutomationRun {
  id: string;
  automation: string;
  target?: string;
  trigger: "schedule" | "manual";
  status: "running" | "succeeded" | "failed" | "skipped";
  started_at_ms: number;
  finished_at_ms?: number;
  cost_usd?: number;
  summary?: string;
  error?: string;
  report_path?: string;
}

export interface MetricTarget {
  metric_id: string;
  initiative_id: string;
  code: string;
  name: string;
  source: string;
  cadence: string;
  last_at?: string;
  next_at?: string;
  skipped?: string;
}

export interface EvalSettings {
  enabled: boolean;
  model: string;
  suite?: string;
  cases?: string[];
  interval_hours?: number;
  max_cost_usd: number;
  baseline?: string;
}

export interface Automation {
  name: "linear_inbound" | "metric_measurement" | "eval";
  label: string;
  description: string;
  enabled: boolean;
  available: boolean;
  unavailable?: string;
  schedule: string;
  running: boolean;
  last_run: AutomationRun | null;
  next_run_at: string | null;
  settings?: EvalSettings;
  targets?: MetricTarget[];
  history: AutomationRun[];
  note?: string;
}

// relative renders a time against now: "in 3h", "12m ago", "just now".
export function relative(at: Date, now: Date): string {
  const s = Math.round((at.getTime() - now.getTime()) / 1000);
  const abs = Math.abs(s);
  if (abs < 45) return "just now";
  const unit = abs < 3600 ? [Math.round(abs / 60), "m"] : abs < 86400 * 2 ? [Math.round(abs / 3600), "h"] : [Math.round(abs / 86400), "d"];
  return s > 0 ? `in ${unit[0]}${unit[1]}` : `${unit[0]}${unit[1]} ago`;
}

export function nextRunText(a: Automation, now: Date): string {
  if (!a.enabled) return "Off — nothing runs until you turn it on";
  if (a.running) return "Running now";
  if (!a.next_run_at) return "Not scheduled";
  const at = new Date(a.next_run_at);
  return at.getTime() <= now.getTime() ? "Due now" : `Next ${relative(at, now)}`;
}

export function lastRunText(a: Automation, now: Date): string {
  const r = a.last_run;
  if (!r) return "Never run";
  return `Last ${statusWord(r)} ${relative(new Date(r.started_at_ms), now)}`;
}

export function statusWord(r: AutomationRun): string {
  switch (r.status) {
    case "succeeded":
      return "succeeded";
    case "failed":
      return "failed";
    case "skipped":
      return "skipped";
    default:
      return "running";
  }
}

// runCost renders a run's cost: "$0.42", "free" for a measured $0, "—"
// when not recorded.
export function runCost(r: AutomationRun): string {
  if (r.cost_usd === undefined || r.cost_usd === null) return "—";
  if (r.cost_usd === 0) return "$0";
  return r.cost_usd < 0.01 ? `$${r.cost_usd.toFixed(4)}` : `$${r.cost_usd.toFixed(2)}`;
}

export function runDetail(r: AutomationRun): string {
  return r.error || r.summary || "";
}

export interface EvalForm {
  model: string;
  maxCostUSD: string;
  intervalHours: string;
  suite: string;
  cases: string;
  baseline: string;
}

export function evalFormFrom(s: EvalSettings | undefined): EvalForm {
  return {
    model: s?.model ?? "",
    maxCostUSD: s?.max_cost_usd ? String(s.max_cost_usd) : "",
    intervalHours: s?.interval_hours ? String(s.interval_hours) : "",
    suite: s?.suite ?? "",
    cases: (s?.cases ?? []).join(", "),
    baseline: s?.baseline ?? "",
  };
}

export function validateEvalForm(f: EvalForm): Record<string, string> {
  const e: Record<string, string> = {};
  const [p, m] = splitSpec(f.model);
  if (!p || !m) e.model = "provider/model, e.g. openai/gpt-4o-mini.";
  const cost = Number(f.maxCostUSD);
  if (f.maxCostUSD.trim() === "" || !Number.isFinite(cost) || cost <= 0) {
    e.maxCostUSD = "Required: the most one run may spend, in dollars (> 0).";
  }
  if (f.intervalHours.trim() !== "") {
    const h = Number(f.intervalHours);
    if (!Number.isInteger(h) || h < 1) e.intervalHours = "Whole hours, at least 1 (default 24).";
  }
  for (const [k, v] of [
    ["suite", f.suite],
    ["baseline", f.baseline],
  ] as const) {
    if (v.trim().startsWith("/") || v.includes("..")) e[k] = "A path relative to the project root, without ..";
  }
  return e;
}

function splitSpec(spec: string): [string, string] {
  const s = spec.trim();
  const i = s.indexOf("/");
  return i < 0 ? ["", s] : [s.slice(0, i), s.slice(i + 1)];
}

export function evalPutBody(f: EvalForm): Record<string, unknown> {
  return {
    model: f.model.trim(),
    max_cost_usd: Number(f.maxCostUSD),
    interval_hours: f.intervalHours.trim() ? Number(f.intervalHours) : 0,
    suite: f.suite.trim(),
    cases: f.cases
      .split(/[\s,]+/)
      .map((c) => c.trim())
      .filter(Boolean),
    baseline: f.baseline.trim(),
  };
}
