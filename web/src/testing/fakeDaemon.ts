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
    return new Response(JSON.stringify(routes[key]), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return { requests, fetchMock };
}
