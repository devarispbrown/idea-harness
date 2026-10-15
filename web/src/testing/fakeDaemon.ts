import { vi } from "vitest";

// fakeDaemon stubs `fetch` for component tests (jsdom): each route maps a
// "METHOD /api/v1/path" prefix to a JSON body, and every request is recorded
// so a test can assert what a component actually sent. An unmatched request
// answers 404, like the daemon would for a route it does not serve.
export interface RecordedRequest {
  method: string;
  path: string;
  body: unknown;
}

// fail is a route body that answers an error instead: the daemon's own
// {"error": "..."} shape with a non-2xx status, so a test can check what a
// component shows when a write is refused.
export class FakeFailure {
  constructor(
    readonly status: number,
    readonly error: string,
  ) {}
}
export const fail = (status: number, error: string) => new FakeFailure(status, error);

export function fakeDaemon(routes: Record<string, unknown>) {
  const requests: RecordedRequest[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    const path = url.replace(/^https?:\/\/[^/]+/, "").split("?")[0];
    const method = (init?.method ?? "GET").toUpperCase();
    let body: unknown = undefined;
    if (typeof init?.body === "string") {
      try {
        body = JSON.parse(init.body);
      } catch {
        body = init.body;
      }
    }
    requests.push({ method, path, body });
    // Longest matching key wins, so a specific route beats a prefix.
    const key = Object.keys(routes)
      .filter((k) => {
        const [m, p] = k.split(" ");
        return m === method && (path === p || path.startsWith(p + "?"));
      })
      .sort((a, b) => b.length - a.length)[0];
    if (!key) {
      return new Response(JSON.stringify({ error: `no fake for ${method} ${path}` }), { status: 404 });
    }
    const answer = routes[key];
    if (answer instanceof FakeFailure) {
      return new Response(JSON.stringify({ error: answer.error }), {
        status: answer.status,
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response(JSON.stringify(answer), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return { requests, fetchMock };
}
