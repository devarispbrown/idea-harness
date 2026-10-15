// Pure helpers for the Objectives & Outcomes surfaces (DefinePage's
// Objectives section, HealthPage's Outcomes panel). Dependency-free so they
// can be unit tested without a DOM.
import type {
  MetricDirection,
  MetricDrift,
  MetricEdit,
  MetricSource,
  MetricSourceKind,
  Objective,
  ObjectiveEdit,
  ObjectiveStatus,
  OutcomeGaps,
  OutcomeMetric,
} from "./api";

// driftLabel turns the wire drift value into readable text for the badge.
export function driftLabel(d: MetricDrift | string): string {
  switch (d) {
    case "on_track":
      return "on track";
    case "off_track":
      return "off track";
    case "regressed":
      return "regressed";
    case "stale":
      return "stale";
    case "no_data":
      return "no data";
    default:
      return d.replace(/_/g, " ");
  }
}

// driftClass maps a drift value to the idchip colour class the badge uses.
export function driftClass(d: MetricDrift | string): string {
  switch (d) {
    case "on_track":
      return "pill-verified";
    case "off_track":
    case "regressed":
      return "chip-failing";
    case "stale":
      return "chip-needs-you";
    default:
      return "chip-inactive";
  }
}

// DRIFT_ORDER ranks the states that need a human's attention, worst first.
const DRIFT_ORDER: Record<string, number> = { regressed: 0, off_track: 1, stale: 2 };

// isDrifting is true for a metric the Health page should call out: off
// track, regressed or stale. no_data is a gap, not drift.
export function isDrifting(m: Pick<OutcomeMetric, "status">): boolean {
  return !!m.status && Object.prototype.hasOwnProperty.call(DRIFT_ORDER, m.status.drift);
}

// driftingMetrics returns the drifting metrics, worst first, then by code.
export function driftingMetrics<T extends Pick<OutcomeMetric, "status" | "code">>(metrics: T[]): T[] {
  return metrics
    .filter(isDrifting)
    .sort(
      (a, b) =>
        DRIFT_ORDER[a.status.drift] - DRIFT_ORDER[b.status.drift] ||
        a.code.localeCompare(b.code, undefined, { numeric: true }),
    );
}

// metricsByObjective groups metrics under their objective id.
export function metricsByObjective<T extends Pick<OutcomeMetric, "objective_id">>(
  metrics: T[],
): Record<string, T[]> {
  const out: Record<string, T[]> = {};
  for (const m of metrics) (out[m.objective_id] ??= []).push(m);
  return out;
}

export interface RequirementLike {
  id: string;
  code: string;
  status?: string;
  objective_ids?: string[] | null;
}

// liveRequirement excludes requirements a PRD revision removed or superseded.
function liveRequirement(r: RequirementLike): boolean {
  return r.status !== "removed" && r.status !== "superseded";
}

// requirementCodes maps requirement ids to their codes, keeping an unknown
// id as-is rather than dropping it (nothing is silently hidden).
export function requirementCodes(ids: string[] | null | undefined, reqs: RequirementLike[]): string[] {
  const byId = new Map(reqs.map((r) => [r.id, r.code]));
  return (ids ?? []).map((id) => byId.get(id) ?? id);
}

// requirementsWithoutObjective returns live requirements that serve no
// objective -- the gap Define surfaces under the Objectives section.
export function requirementsWithoutObjective<T extends RequirementLike>(reqs: T[]): T[] {
  return reqs.filter((r) => liveRequirement(r) && (r.objective_ids ?? []).length === 0);
}

// linkableRequirements returns the live requirements not yet linked to the
// given objective, for the "link a requirement" picker.
export function linkableRequirements<T extends RequirementLike>(
  reqs: T[],
  linkedIds: string[] | null | undefined,
): T[] {
  const linked = new Set(linkedIds ?? []);
  return reqs.filter((r) => liveRequirement(r) && !linked.has(r.id));
}

const DIRECTION_ARROW: Record<MetricDirection, string> = {
  increase: "↑",
  decrease: "↓",
  maintain: "=",
};

export function directionArrow(d: MetricDirection | string): string {
  return DIRECTION_ARROW[d as MetricDirection] ?? d;
}

function withUnit(v: number, unit?: string): string {
  if (!unit) return String(v);
  return unit === "%" ? `${v}%` : `${v} ${unit}`;
}

// metricRange renders "baseline → target unit" (or "→ target" without a
// baseline), prefixed by the direction arrow, e.g. "↑ 12% → 20%".
export function metricRange(
  m: Pick<OutcomeMetric, "direction" | "baseline" | "target" | "unit">,
): string {
  const target = withUnit(m.target, m.unit);
  const from = m.baseline !== undefined && m.baseline !== null ? `${withUnit(m.baseline, m.unit)} → ` : "→ ";
  return `${directionArrow(m.direction)} ${from}${target}`;
}

// outcomeGapRows turns the report's gaps into labelled rows, skipping empty
// ones, in the chain's order: objective → metric, objective → requirement,
// requirement → objective.
export function outcomeGapRows(gaps: OutcomeGaps | null | undefined): { label: string; codes: string[] }[] {
  if (!gaps) return [];
  const rows = [
    { label: "Objectives with no metric", codes: gaps.objectives_without_metrics ?? [] },
    { label: "Objectives no requirement serves", codes: gaps.objectives_without_requirements ?? [] },
    { label: "Requirements serving no objective", codes: gaps.requirements_without_objectives ?? [] },
  ];
  return rows.filter((r) => r.codes.length > 0);
}

// parseNumber reads a numeric form field: undefined for blank or
// non-numeric input, so a caller can tell "omitted" from "invalid".
export function parseNumber(s: string): number | undefined {
  const t = s.trim();
  if (!t) return undefined;
  const n = Number(t);
  return Number.isFinite(n) ? n : undefined;
}

// ---------- edit forms (Define's Objectives section) ----------

export interface ObjectiveDraft {
  statement: string;
  rationale: string;
  owner: string;
  status: ObjectiveStatus;
}

export function objectiveDraft(o: Pick<Objective, "statement" | "rationale" | "owner" | "status">): ObjectiveDraft {
  return { statement: o.statement, rationale: o.rationale ?? "", owner: o.owner ?? "", status: o.status };
}

// objectiveEditBody is the PATCH body for what the draft changed: only the
// changed fields, so saving an untouched form changes nothing (and an edit
// races less with one made from the CLI). An error when the statement was
// emptied -- an objective without one is not an objective.
export function objectiveEditBody(
  o: Pick<Objective, "statement" | "rationale" | "owner" | "status">,
  d: ObjectiveDraft,
): { body: ObjectiveEdit } | { error: string } {
  const statement = d.statement.trim();
  if (!statement) return { error: "An objective needs a statement." };
  const body: ObjectiveEdit = {};
  if (statement !== o.statement) body.statement = statement;
  if (d.rationale.trim() !== (o.rationale ?? "")) body.rationale = d.rationale.trim();
  if (d.owner.trim() !== (o.owner ?? "")) body.owner = d.owner.trim();
  if (d.status !== o.status) body.status = d.status;
  return { body };
}

// MetricDraft is the metric form's state: every field the metric edit API
// takes, as typed. The add form shows the first five; the edit form all of
// them.
export interface MetricDraft {
  name: string;
  direction: MetricDirection;
  target: string;
  baseline: string;
  unit: string;
  guardrail: string;
  cadence: string;
  definition: string;
  // objective is the code of the objective the metric measures; changing it
  // moves the metric.
  objective: string;
  sourceKind: MetricSourceKind;
  command: string;
  url: string;
  pointer: string;
}

type MetricLike = Pick<OutcomeMetric, "name" | "direction" | "target" | "baseline" | "unit"> &
  Partial<Pick<OutcomeMetric, "guardrail" | "cadence" | "definition" | "source" | "objective_code">>;

const numText = (v: number | null | undefined) => (v === undefined || v === null ? "" : String(v));

export function metricDraft(m: MetricLike): MetricDraft {
  const src = m.source ?? { kind: "manual" as const };
  return {
    name: m.name,
    direction: m.direction,
    target: String(m.target),
    baseline: numText(m.baseline),
    unit: m.unit ?? "",
    guardrail: numText(m.guardrail),
    cadence: m.cadence ?? "",
    definition: m.definition ?? "",
    objective: m.objective_code ?? "",
    sourceKind: src.kind,
    command: src.command ?? "",
    url: src.url ?? "",
    pointer: src.pointer ?? "",
  };
}

// CADENCE_WORDS and parseCadence mirror domain.ParseCadence: a named cadence,
// <n>d, <n>w, or a positive Go duration ("36h", "90m", "1h30m"); blank is no
// cadence. The form refuses what the API would, before the round trip.
const CADENCE_WORDS = new Set(["hourly", "daily", "weekly", "biweekly", "monthly", "quarterly"]);
const GO_DURATION = /^\+?((\d+(\.\d*)?|\.\d+)(ns|us|µs|ms|s|m|h))+$/;

export function cadenceError(s: string): string | undefined {
  const c = s.trim().toLowerCase();
  if (!c || CADENCE_WORDS.has(c)) return undefined;
  const nd = /^(\d+)[dw]$/.exec(c);
  if (nd && Number(nd[1]) > 0) return undefined;
  if (GO_DURATION.test(c) && /[1-9]/.test(c)) return undefined;
  return `Cadence "${s.trim()}" is not hourly, daily, weekly, biweekly, monthly, quarterly, <n>d, <n>w or a duration like 36h.`;
}

// metricSourceFromDraft builds the source the draft describes, or the reason
// the API would refuse it (domain.MetricSource.Validate).
export function metricSourceFromDraft(d: MetricDraft): { source: MetricSource } | { error: string } {
  switch (d.sourceKind) {
    case "manual":
      return { source: { kind: "manual" } };
    case "command": {
      const command = d.command.trim();
      if (!command) return { error: "A command source needs a command." };
      return { source: { kind: "command", command } };
    }
    case "url": {
      const url = d.url.trim();
      const pointer = d.pointer.trim();
      if (!url.startsWith("https://") && !url.startsWith("http://")) return { error: "A URL source needs an http(s) URL." };
      if (pointer && !pointer.startsWith("/")) return { error: `Pointer "${pointer}" is not a JSON pointer (it must start with /).` };
      return { source: pointer ? { kind: "url", url, pointer } : { kind: "url", url } };
    }
  }
  return { error: `Unknown source kind "${d.sourceKind}".` };
}

const sameSource = (a: MetricSource | undefined, b: MetricSource) =>
  (a?.kind ?? "manual") === b.kind &&
  (a?.command ?? "") === (b.command ?? "") &&
  (a?.url ?? "") === (b.url ?? "") &&
  (a?.pointer ?? "") === (b.pointer ?? "");

// metricEditBody is metric edit's PATCH body: the changed fields only, after
// the same validation the API applies (store.ValidateMetric). A baseline or
// guardrail emptied in the form is clear_baseline / clear_guardrail (an
// omitted field would leave it as it was); a non-numeric number is an error,
// not a silently dropped field; a changed source is sent whole, as the API
// replaces it whole.
export function metricEditBody(m: MetricLike, d: MetricDraft): { body: MetricEdit } | { error: string } {
  const name = d.name.trim();
  if (!name) return { error: "A metric needs a name." };
  const target = parseNumber(d.target);
  if (target === undefined) return { error: "Target must be a number." };
  const hadBaseline = m.baseline !== undefined && m.baseline !== null;
  const baseline = parseNumber(d.baseline);
  if (d.baseline.trim() && baseline === undefined) return { error: "Baseline must be a number (or blank)." };
  const hadGuardrail = m.guardrail !== undefined && m.guardrail !== null;
  const guardrail = parseNumber(d.guardrail);
  if (d.guardrail.trim() && guardrail === undefined) return { error: "Guardrail must be a number (or blank)." };
  if (guardrail !== undefined && d.direction === "maintain" && guardrail < 0) {
    return { error: "A maintain metric's guardrail is the allowed distance from the target and cannot be negative." };
  }
  const cadErr = cadenceError(d.cadence);
  if (cadErr) return { error: cadErr };
  const src = metricSourceFromDraft(d);
  if ("error" in src) return { error: src.error };

  const body: MetricEdit = {};
  if (name !== m.name) body.name = name;
  if (d.direction !== m.direction) body.direction = d.direction;
  if (target !== m.target) body.target = target;
  if (baseline === undefined && hadBaseline) body.clear_baseline = true;
  else if (baseline !== undefined && baseline !== m.baseline) body.baseline = baseline;
  if (guardrail === undefined && hadGuardrail) body.clear_guardrail = true;
  else if (guardrail !== undefined && guardrail !== m.guardrail) body.guardrail = guardrail;
  if (d.unit.trim() !== (m.unit ?? "")) body.unit = d.unit.trim();
  if (d.cadence.trim() !== (m.cadence ?? "")) body.cadence = d.cadence.trim();
  if (d.definition.trim() !== (m.definition ?? "")) body.definition = d.definition.trim();
  const objective = d.objective.trim();
  if (objective && objective !== (m.objective_code ?? "")) body.objective = objective;
  if (!sameSource(m.source, src.source)) body.source = src.source;
  return { body };
}
