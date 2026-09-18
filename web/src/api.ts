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
