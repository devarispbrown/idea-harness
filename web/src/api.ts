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
