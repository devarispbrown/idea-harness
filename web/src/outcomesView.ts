// Pure logic for the Outcomes dashboard (#/i/<id>/outcomes): progress from
// baseline to target, sparkline geometry (inline SVG, no chart library), and
// which change sets landed between observations. OutcomesPage.tsx renders
// what these return; outcomesView.test.ts covers them without a DOM.

import type { MetricObservation, OutcomeMetric } from "./api";

export interface Progress {
  // 0..1 share of the way from baseline to target, or null when it cannot be
  // said (no baseline, no value yet, a maintain metric, target == baseline).
  fraction: number | null;
  // Past the target in the metric's own direction.
  achieved: boolean;
}

// metricProgress places the latest value on the baseline → target line. The
// baseline is the daemon's effective one (declared, else the first
// observation), so a metric with no declared baseline still gets a bar once
// it has two readings. Moving the wrong way clamps at 0 rather than going
// negative; drift says how bad.
export function metricProgress(m: Pick<OutcomeMetric, "direction" | "target" | "baseline" | "status">): Progress {
  const latest = m.status?.latest;
  const base = m.status?.effective_baseline ?? m.baseline;
  if (latest === undefined || latest === null) return { fraction: null, achieved: false };
  const achieved = !!m.status?.achieved;
  if (m.direction === "maintain" || base === undefined || base === null || base === m.target) {
    return { fraction: null, achieved };
  }
  // The daemon's `achieved` is authoritative: a target already met reads as
  // the full bar even when a first-observation baseline sits past it.
  if (achieved) return { fraction: 1, achieved };
  const f = (latest - base) / (m.target - base);
  return { fraction: Math.max(0, Math.min(1, f)), achieved: f >= 1 };
}

export interface SparkPoint {
  x: number;
  y: number;
  value: number;
}

export interface Sparkline {
  points: SparkPoint[];
  path: string; // the polyline's `points` attribute
  // y of the target and baseline lines when they fall inside the box (they
  // always do: the scale includes them), undefined when not given.
  targetY?: number;
  baselineY?: number;
  min: number;
  max: number;
}

// sparkline scales a series into a width x height box with `pad` pixels of
// margin. The vertical domain includes the target and baseline so the line is
// read against them, not just against itself; a flat domain (every value
// equal) is centred instead of dividing by zero. Values are plotted at equal
// x spacing in their given order -- observation index, not wall time, so a
// burst of readings on one day stays legible. One value is centred.
export function sparkline(
  values: number[],
  width: number,
  height: number,
  opts: { pad?: number; target?: number; baseline?: number } = {},
): Sparkline {
  const pad = opts.pad ?? 3;
  const domain = [...values];
  if (opts.target !== undefined) domain.push(opts.target);
  if (opts.baseline !== undefined) domain.push(opts.baseline);
  const min = domain.length ? Math.min(...domain) : 0;
  const max = domain.length ? Math.max(...domain) : 0;
  const innerW = Math.max(0, width - pad * 2);
  const innerH = Math.max(0, height - pad * 2);
  const y = (v: number) => (max === min ? pad + innerH / 2 : pad + innerH - ((v - min) / (max - min)) * innerH);
  const x = (i: number) => (values.length <= 1 ? pad + innerW / 2 : pad + (i / (values.length - 1)) * innerW);
  const points = values.map((v, i) => ({ x: round(x(i)), y: round(y(v)), value: v }));
  return {
    points,
    path: points.map((p) => `${p.x},${p.y}`).join(" "),
    targetY: opts.target !== undefined ? round(y(opts.target)) : undefined,
    baselineY: opts.baseline !== undefined ? round(y(opts.baseline)) : undefined,
    min,
    max,
  };
}

const round = (n: number) => Math.round(n * 100) / 100;

// byObservedAt orders observations oldest first. The daemon returns them in
// that order already; sorting here keeps a backdated reading (`--at`) in its
// place regardless.
export function byObservedAt<T extends Pick<MetricObservation, "observed_at">>(obs: T[]): T[] {
  return [...(obs ?? [])].sort((a, b) => Date.parse(a.observed_at) - Date.parse(b.observed_at));
}

export interface ObservationRow {
  observation: MetricObservation;
  delta?: number; // vs. the previous observation
  // Change sets (human ids when known) that landed since the previous
  // observation -- the daemon attributes them at record time
  // (observation.change_set_ids).
  changeSets: { id: string; code: string }[];
}

// observationRows pairs each observation with its delta and the change sets
// that landed before it, newest first for the history table.
export function observationRows(obs: MetricObservation[], changeSetCodes: Record<string, string>): ObservationRow[] {
  const sorted = byObservedAt(obs);
  const rows = sorted.map((o, i) => ({
    observation: o,
    delta: i > 0 ? round(o.value - sorted[i - 1].value) : undefined,
    changeSets: (o.change_set_ids ?? []).map((id) => ({ id, code: changeSetCodes[id] ?? id.slice(0, 8) })),
  }));
  return rows.reverse();
}

// isMeasurable is true for a metric the daemon can read itself (command or
// url source) -- the ones that get a Measure button. Manual metrics get the
// record form only.
export function isMeasurable(m: Pick<OutcomeMetric, "source">): boolean {
  return m.source?.kind === "command" || m.source?.kind === "url";
}

// formatValue renders a number with the metric's unit.
export function formatValue(v: number | undefined | null, unit?: string): string {
  if (v === undefined || v === null) return "—";
  const n = Number.isInteger(v) ? String(v) : String(round(v));
  if (!unit) return n;
  return unit === "%" ? `${n}%` : `${n} ${unit}`;
}

// sourceLabel says where a metric's values come from.
export function sourceLabel(m: Pick<OutcomeMetric, "source">): string {
  const s = m.source ?? { kind: "manual" };
  if (s.kind === "command" && typeof s.command === "string") return `command: ${s.command}`;
  if (s.kind === "url" && typeof s.url === "string") return `url: ${s.url}`;
  return s.kind ?? "manual";
}
