import { describe, expect, it } from "vitest";
import { unwrapError } from "./api";

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
