// Guarded for environments with no `document` (vitest's default "node"
// environment, used by this file's own unit tests) so importing `unwrapError`
// for testing doesn't require pulling in jsdom just to satisfy a module-level
// side effect this file needs only in the browser.
export const token =
  typeof document !== "undefined"
    ? (document.querySelector('meta[name="idea-token"]')?.getAttribute("content") ?? "")
    : "";

// unwrapError turns a non-OK response body into the message the UI should
// show. The daemon's error responses are `{"error": "..."}`; without this,
// every failed call surfaced as `Error: {"error":"...<provider>..."}` because
// the raw JSON text was passed straight into `new Error(...)`. Every other
// worker's error-handling codes against this contract (F2), so it is pure and
// exported: unwrap {"error": "..."} to the message alone, and fall back to
// the raw text, and then to "<status> <statusText>", for anything else.
export function unwrapError(text: string, status: number, statusText: string): string {
  if (!text) return `${status} ${statusText}`;
  try {
    const parsed: unknown = JSON.parse(text);
    if (
      parsed &&
      typeof parsed === "object" &&
      "error" in parsed &&
      typeof (parsed as { error: unknown }).error === "string" &&
      (parsed as { error: string }).error
    ) {
      return (parsed as { error: string }).error;
    }
  } catch {
    // Not JSON (or not an {"error": "..."} shape) -- fall through to the raw
    // text, which is still more useful than nothing.
  }
  return text;
}

// NetworkError marks a request that never got an HTTP response at all --
// fetch() itself rejected (DNS failure, connection refused, the daemon
// process is simply gone), as opposed to a response that arrived and said
// something like 404 or 500. M4: only THIS is evidence the daemon is down;
// a 404/500 is the daemon answering just fine, about something else. Its
// `.message` is deliberately whatever the underlying rejection said (or
// `String(e)` for a non-Error rejection) rather than a fixed string, so a
// caller that just does `e instanceof Error ? e.message : String(e)` still
// gets a sensible message either way.
export class NetworkError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "NetworkError";
  }
}

// callFetch wraps `fetch` so a rejection (no response reached at all) throws
// NetworkError specifically, distinguishable from the plain Error that
// throwOnError below throws for a response that arrived but wasn't ok.
async function callFetch(input: string, init?: RequestInit): Promise<Response> {
  try {
    return await fetch(input, init);
  } catch (e) {
    throw new NetworkError(e instanceof Error ? e.message : String(e));
  }
}

async function throwOnError(res: Response): Promise<void> {
  if (!res.ok) {
    const text = await res.text();
    throw new Error(unwrapError(text, res.status, res.statusText));
  }
}

export async function api<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const res = await callFetch("/api/v1" + path, {
    method,
    headers: {
      "Content-Type": "application/json",
      "X-Idea-Token": token,
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  await throwOnError(res);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// apiRaw returns a text response (used for artifact export).
export async function apiRaw(method: string, path: string): Promise<string> {
  const res = await callFetch("/api/v1" + path, {
    method,
    headers: { "X-Idea-Token": token },
  });
  await throwOnError(res);
  return res.text();
}

// resource builds the URL of an initiative-scoped resource:
//
//   /projects/{projectId}/initiatives/{initiativeId}/{name}
//
// One builder rather than a template literal per call site. The flat
// `?initiative_id=` form this replaces was hand-rolled at all seventeen call
// sites, which is how the API ended up with two shapes for the same question --
// and this is what stops the eighteenth site from reintroducing it.
//
// Both ids are encoded. In query position an id containing a slash or ".." was
// inert; in path position fetch() normalizes it, so encoding is load-bearing
// rather than tidiness. Only one of the seventeen sites encoded anything.
export const resource = (
  projectId: string,
  initiativeId: string,
  name: string,
) =>
  `/projects/${encodeURIComponent(projectId)}/initiatives/${encodeURIComponent(
    initiativeId,
  )}/${name}`;

export const get = <T>(path: string) => api<T>("GET", path);
export const post = <T>(path: string, body: unknown) =>
  api<T>("POST", path, body);
export const put = <T>(path: string, body: unknown) =>
  api<T>("PUT", path, body);
// del is "Start over" (C3: DELETE .../partial-work) -- the one DELETE this
// UI makes today, so it gets the same one-liner as get/post/put rather than
// a call site building its own `api<T>("DELETE", ...)`.
export const del = <T>(path: string) => api<T>("DELETE", path);

// apiBlob fetches a binary/text response as a Blob (context-doc download).
export async function apiBlob(path: string): Promise<Blob> {
  const res = await callFetch("/api/v1" + path, {
    headers: { "X-Idea-Token": token },
  });
  await throwOnError(res);
  return res.blob();
}

// uploadDoc posts a multipart file upload (context-doc ingest). The daemon
// sniffs the format and extracts text from .md/.txt/.pdf/.docx.
export async function uploadDoc<T>(
  path: string,
  file: File,
  fields: Record<string, string>,
): Promise<T> {
  const form = new FormData();
  form.append("file", file, file.name);
  for (const [k, v] of Object.entries(fields)) {
    if (v) form.append(k, v);
  }
  const res = await callFetch("/api/v1" + path, {
    method: "POST",
    headers: { "X-Idea-Token": token },
    body: form,
  });
  await throwOnError(res);
  return (await res.json()) as T;
}

// patch mirrors get/post/put for the PATCH routes (objectives, metrics).
export const patch = <T>(path: string, body: unknown) =>
  api<T>("PATCH", path, body);

// --- Objectives and outcomes -------------------------------------------------
// The outcome end of the Product Graph (docs/api.md "Objectives and
// outcomes"): objectives (OBJ-n), the metrics that measure them (MET-n), and
// their observations. Every write here is a human edit.

export type ObjectiveStatus = "active" | "achieved" | "abandoned";

export interface Objective {
  id: string;
  code: string;
  statement: string;
  rationale?: string;
  status: ObjectiveStatus;
  owner?: string;
  requirement_ids: string[];
  metric_ids: string[];
}

export type MetricDirection = "increase" | "decrease" | "maintain";
export type MetricDrift = "on_track" | "off_track" | "stale" | "regressed" | "no_data";

export interface MetricStatus {
  drift: MetricDrift;
  off_track: boolean;
  stale: boolean;
  regressed: boolean;
  guardrail_breached: boolean;
  achieved: boolean;
  observations: number;
  latest?: number;
  latest_at?: string;
  previous?: number;
  effective_baseline?: number;
  baseline_source?: string;
  regressed_by?: string[];
  next_due_at?: string;
  reasons?: string[];
}

export interface MetricSource {
  kind: "manual" | "command" | "url";
  [key: string]: unknown;
}

export interface OutcomeMetric {
  id: string;
  code: string;
  objective_id: string;
  objective_code: string;
  name: string;
  unit?: string;
  direction: MetricDirection;
  baseline?: number;
  target: number;
  guardrail?: number;
  cadence?: string;
  source: MetricSource;
  status: MetricStatus;
}

export interface RequirementOutcomeDrift {
  requirement_code: string;
  objective_code: string;
  metric_codes: string[];
  signal: "works_but_fails_objective";
  detail: string;
}

export interface OutcomeSummary {
  objectives: number;
  metrics: number;
  on_track: number;
  off_track: number;
  regressed: number;
  stale: number;
  no_data: number;
  drifting: number;
  works_but_fails: number;
}

export interface OutcomeGaps {
  objectives_without_metrics?: string[];
  objectives_without_requirements?: string[];
  requirements_without_objectives?: string[];
}

export interface OutcomeReport {
  objectives: Objective[];
  metrics: OutcomeMetric[];
  requirement_drift?: RequirementOutcomeDrift[];
  gaps?: OutcomeGaps;
  summary: OutcomeSummary;
}

export interface NewObjective {
  statement: string;
  rationale?: string;
  owner?: string;
}

export interface ObjectiveEdit {
  statement?: string;
  rationale?: string;
  owner?: string;
  status?: ObjectiveStatus;
}

export interface NewMetric {
  objective: string;
  name: string;
  direction: MetricDirection;
  target: number;
  baseline?: number;
  guardrail?: number;
  unit?: string;
  cadence?: string;
  hypothesis?: string;
}

export interface MetricObservation {
  id: string;
  metric_id: string;
  value: number;
  observed_at: string;
  note?: string;
  recorded_by: string;
  change_set_ids: string[];
}

// Objective and metric ids in paths accept the code (OBJ-1, MET-2) or the id.
const seg = (id: string) => encodeURIComponent(id);

export const listObjectives = (projectId: string, initiativeId: string) =>
  get<Objective[]>(resource(projectId, initiativeId, "objectives"));

export const createObjective = (projectId: string, initiativeId: string, body: NewObjective) =>
  post<Objective>(resource(projectId, initiativeId, "objectives"), body);

export const updateObjective = (
  projectId: string,
  initiativeId: string,
  objectiveId: string,
  body: ObjectiveEdit,
) => patch<Objective>(resource(projectId, initiativeId, `objectives/${seg(objectiveId)}`), body);

export const linkObjectiveRequirement = (
  projectId: string,
  initiativeId: string,
  objectiveId: string,
  requirement: string,
  unlink = false,
) =>
  post<Objective>(
    resource(projectId, initiativeId, `objectives/${seg(objectiveId)}/requirements`),
    unlink ? { requirement, unlink } : { requirement },
  );

export const listMetrics = (projectId: string, initiativeId: string) =>
  get<OutcomeMetric[]>(resource(projectId, initiativeId, "metrics"));

export const createMetric = (projectId: string, initiativeId: string, body: NewMetric) =>
  post<OutcomeMetric>(resource(projectId, initiativeId, "metrics"), body);

// MetricEdit is the PATCH .../metrics/{id} body (docs/api.md): only the
// fields being changed. clear_baseline / clear_guardrail remove a value,
// which an omitted field cannot say.
export interface MetricEdit {
  name?: string;
  direction?: MetricDirection;
  target?: number;
  baseline?: number;
  clear_baseline?: boolean;
  guardrail?: number;
  clear_guardrail?: boolean;
  unit?: string;
}

export const updateMetric = (projectId: string, initiativeId: string, metricId: string, body: MetricEdit) =>
  patch<OutcomeMetric>(resource(projectId, initiativeId, `metrics/${seg(metricId)}`), body);

export const recordObservation = (
  projectId: string,
  initiativeId: string,
  metricId: string,
  value: number,
  note?: string,
) =>
  post<{ observation: MetricObservation; status: MetricStatus }>(
    resource(projectId, initiativeId, `metrics/${seg(metricId)}/observations`),
    note ? { value, note } : { value },
  );

export const getOutcomes = (projectId: string, initiativeId: string) =>
  get<OutcomeReport>(resource(projectId, initiativeId, "outcomes"));
