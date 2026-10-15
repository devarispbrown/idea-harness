// Pure helpers for the Objectives & Outcomes surfaces (DefinePage's
// Objectives section, HealthPage's Outcomes panel). Dependency-free so they
// can be unit tested without a DOM.
import type { MetricDirection, MetricDrift, OutcomeGaps, OutcomeMetric } from "./api";

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
