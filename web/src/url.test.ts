import { describe, expect, it } from "vitest";
import { isHttpUrl } from "./url";

// H5, review fix: pr_url and PR check links are relayed from `gh` output and
// a third-party CI provider, and used as an `<a href>` -- this is the gate
// that keeps a non-http(s) scheme from ever reaching one.
describe("isHttpUrl", () => {
  it("accepts http and https URLs", () => {
    expect(isHttpUrl("https://github.com/org/repo/pull/1")).toBe(true);
    expect(isHttpUrl("http://ci.example.com/checks/1")).toBe(true);
  });

  it("accepts a scheme in any case", () => {
    expect(isHttpUrl("HTTPS://github.com/org/repo/pull/1")).toBe(true);
  });

  it("rejects a javascript: URL", () => {
    expect(isHttpUrl("javascript:alert(1)")).toBe(false);
  });

  it("rejects other non-http(s) schemes", () => {
    expect(isHttpUrl("data:text/html,<script>alert(1)</script>")).toBe(false);
    expect(isHttpUrl("vbscript:msgbox(1)")).toBe(false);
    expect(isHttpUrl("file:///etc/passwd")).toBe(false);
  });

  it("rejects a bare string that is not a URL at all", () => {
    expect(isHttpUrl("not a url")).toBe(false);
  });

  it("rejects empty, null and undefined", () => {
    expect(isHttpUrl("")).toBe(false);
    expect(isHttpUrl(null)).toBe(false);
    expect(isHttpUrl(undefined)).toBe(false);
  });

  it("tolerates leading/trailing whitespace around an otherwise valid URL", () => {
    expect(isHttpUrl("  https://example.com  ")).toBe(true);
  });
});
