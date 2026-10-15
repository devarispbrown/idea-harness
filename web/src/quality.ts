// Pure logic for the quality-dimension scorecard (GET .../quality). The
// component (QualityScorecard.tsx) only renders what these return, so the
// rules -- what counts as passing, what to show for a dimension never
// measured, which checks deserve a line -- are tested here without a DOM.

export interface QualityCitation {
  path?: string;
  line?: number;
  requirement?: string;
}

export interface QualityCheck {
  name: string;
  status: string;
  method: string;
  detail?: string;
  citations?: QualityCitation[];
}

export interface QualityResult {
  id: string;
  status: string;
  summary: string;
  checks: QualityCheck[];
  requirements: string[];
  created_at?: string;
}

export interface QualityDimensionRow {
  dimension: string;
  mode: string;
  result: QualityResult | null;
  stale: boolean;
}

export interface QualityScorecard {
  rigor: string;
  overridden: string[];
  dimensions: QualityDimensionRow[];
  needs_measurement: boolean;
  gate_unmet: string[];
}

// statusLabel renders a row's status. A dimension never measured, or measured
// against older work, must not read as its last verdict: "not measured" and
// "(stale)" say so outright.
export function statusLabel(row: QualityDimensionRow): string {
  if (!row.result) return "not measured";
  const s = row.result.status.replace(/_/g, " ");
  return row.stale ? `${s} (stale)` : s;
}

// statusTone maps a status to the chip tone. not_measured is a warning tone,
// never the pass tone -- unmeasured is not green.
export function statusTone(row: QualityDimensionRow): "ok" | "warn" | "bad" | "muted" {
  if (!row.result || row.stale) return "warn";
  switch (row.result.status) {
    case "pass":
      return "ok";
    case "not_applicable":
      return "muted";
    case "fail":
      return "bad";
    default:
      return "warn";
  }
}

// enabledRows is the scorecard minus the dimensions whose mode is off, in the
// daemon's order. offDimensions is the rest, for one muted line.
export function enabledRows(sc: QualityScorecard): QualityDimensionRow[] {
  return sc.dimensions.filter((d) => d.mode !== "off");
}

export function offDimensions(sc: QualityScorecard): string[] {
  return sc.dimensions.filter((d) => d.mode === "off").map((d) => d.dimension);
}

// attentionChecks are the checks worth a line under a dimension: anything
// that is not a pass and not "nothing to judge".
export function attentionChecks(result: QualityResult | null): QualityCheck[] {
  if (!result) return [];
  return result.checks.filter((c) => c.status !== "pass" && c.status !== "not_applicable");
}

// citationLabel renders a check's first citation as path:line (requirement).
export function citationLabel(c: QualityCheck): string {
  const ct = c.citations?.[0];
  if (!ct) return "";
  let s = ct.path ?? "";
  if (ct.path && ct.line) s += `:${ct.line}`;
  if (ct.requirement) s += s ? ` (${ct.requirement})` : ct.requirement;
  return s;
}

// gateLabel is the one-line verdict: whether the quality half of the
// verification gate is met, counting gated dimensions only.
export function gateLabel(sc: QualityScorecard): string {
  const gated = sc.dimensions.filter((d) => d.mode === "gate").length;
  if (gated === 0) return "No dimension is gated — quality is advisory only";
  if (sc.gate_unmet.length === 0) return `All ${gated} gated dimension${gated === 1 ? "" : "s"} met`;
  return `Gate unmet: ${sc.gate_unmet.join("; ")}`;
}
