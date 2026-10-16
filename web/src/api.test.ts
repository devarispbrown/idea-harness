import { describe, expect, it, vi, afterEach } from "vitest";
import { api, NetworkError, unwrapError, uploadInitiative } from "./api";

// F2: api.ts must throw an Error whose .message is the server's `error`
// string when the body is `{"error": "..."}`, and the raw text or status
// otherwise. Every other worker's UI error handling codes against this
// contract, so it is pinned down here in isolation from fetch.
describe("unwrapError", () => {
  it("unwraps an {error} JSON body to the message alone", () => {
    expect(
      unwrapError(
        '{"error":"OPENAI_API_KEY is not set in the daemon\'s environment"}',
        500,
        "Internal Server Error",
      ),
    ).toBe("OPENAI_API_KEY is not set in the daemon's environment");
  });

  it("falls back to the raw text when the body isn't JSON", () => {
    expect(unwrapError("plain text failure", 400, "Bad Request")).toBe("plain text failure");
  });

  it("falls back to '<status> <statusText>' when the body is empty", () => {
    expect(unwrapError("", 503, "Service Unavailable")).toBe("503 Service Unavailable");
  });

  it("falls back to the raw text when the JSON has no string `error` field", () => {
    expect(unwrapError('{"message":"nope"}', 400, "Bad Request")).toBe('{"message":"nope"}');
  });

  it("falls back to the raw text for JSON that isn't an {error} object", () => {
    expect(unwrapError("[1,2,3]", 400, "Bad Request")).toBe("[1,2,3]");
  });

  it("does not treat an empty `error` string as a usable message", () => {
    expect(unwrapError('{"error":""}', 400, "Bad Request")).toBe('{"error":""}');
  });

  it("does not treat a non-string `error` field as a usable message", () => {
    expect(unwrapError('{"error":404}', 400, "Bad Request")).toBe('{"error":404}');
  });
});

// M4: the daemon-down screen must trigger on a network failure (fetch itself
// rejecting -- no response reached at all), never on an HTTP error response
// (a 404/500 IS the daemon answering). This pins down which of the two `api`
// throws in each case.
describe("api / NetworkError", () => {
  const realFetch = globalThis.fetch;
  afterEach(() => {
    globalThis.fetch = realFetch;
  });

  it("throws NetworkError when fetch itself rejects", async () => {
    globalThis.fetch = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    await expect(api("GET", "/whatever")).rejects.toBeInstanceOf(NetworkError);
  });

  it("keeps the underlying message on a NetworkError", async () => {
    globalThis.fetch = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    await expect(api("GET", "/whatever")).rejects.toThrow("Failed to fetch");
  });

  it("throws a plain Error, not NetworkError, for an HTTP error response", async () => {
    globalThis.fetch = vi.fn().mockResolvedValue(
      new Response('{"error":"provider refused the request"}', {
        status: 500,
        statusText: "Internal Server Error",
      }),
    );
    try {
      await api("GET", "/whatever");
      throw new Error("expected api() to throw");
    } catch (e) {
      expect(e).not.toBeInstanceOf(NetworkError);
      expect(e instanceof Error ? e.message : String(e)).toBe("provider refused the request");
    }
  });
});

// uploadInitiative is the file path of the "New initiative" contract: one
// FormData with `title`, `description`, and a repeated `file` part per
// grounding doc -- no Content-Type header of its own, so the browser sets the
// multipart boundary. This pins the shape App.tsx depends on while the
// backend lands in parallel.
describe("uploadInitiative", () => {
  const realFetch = globalThis.fetch;
  afterEach(() => {
    globalThis.fetch = realFetch;
  });

  it("builds multipart form-data with title, description, and repeated file parts", async () => {
    let body: unknown;
    let contentType: string | null = null;
    globalThis.fetch = vi.fn(async (_input, init) => {
      body = init?.body;
      const headers = init?.headers as Record<string, string> | undefined;
      contentType = headers ? (headers["Content-Type"] ?? null) : null;
      return new Response(JSON.stringify({ initiative_id: "i1", operation_id: "op1" }), {
        status: 202,
      });
    });

    const files = [
      new File(["# brief"], "brief.md", { type: "text/markdown" }),
      new File(["x"], "mock.png", { type: "image/png" }),
    ];
    const created = await uploadInitiative<{ initiative_id: string; operation_id: string }>(
      "/projects/p1/initiatives",
      { title: "Add CSV export", description: "Add CSV export\n\nto the reports page" },
      files,
    );

    expect(created).toEqual({ initiative_id: "i1", operation_id: "op1" });
    expect(body).toBeInstanceOf(FormData);
    const form = body as FormData;
    expect(form.get("title")).toBe("Add CSV export");
    expect(form.get("description")).toBe("Add CSV export\n\nto the reports page");
    expect(form.getAll("file")).toHaveLength(2);
    // No JSON Content-Type: the browser must own the multipart boundary.
    expect(contentType).toBeNull();
  });
});
