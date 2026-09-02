export const token =
  document.querySelector('meta[name="idea-token"]')?.getAttribute("content") ?? "";

export async function api<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const res = await fetch("/api/v1" + path, {
    method,
    headers: {
      "Content-Type": "application/json",
      "X-Idea-Token": token,
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || `${res.status} ${res.statusText}`);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// apiRaw returns a text response (used for artifact export).
export async function apiRaw(method: string, path: string): Promise<string> {
  const res = await fetch("/api/v1" + path, {
    method,
    headers: { "X-Idea-Token": token },
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || `${res.status} ${res.statusText}`);
  }
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
  const res = await fetch("/api/v1" + path, {
    headers: { "X-Idea-Token": token },
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || `${res.status} ${res.statusText}`);
  }
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
  const res = await fetch("/api/v1" + path, {
    method: "POST",
    headers: { "X-Idea-Token": token },
    body: form,
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || `${res.status} ${res.statusText}`);
  }
  return (await res.json()) as T;
}
