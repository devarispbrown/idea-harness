import { describe, expect, it } from "vitest";
import { DENY_CODES, denyExplanation, denyFromError, isSystemDeny } from "./denyCodes";

describe("denyExplanation", () => {
  it("returns null for a finding with no deny code (a model finding)", () => {
    expect(denyExplanation(undefined)).toBeNull();
    expect(denyExplanation("")).toBeNull();
  });

  it("explains each known deny code with what/why/next", () => {
    for (const code of ["secret_sweep", "blast_radius", "review_open_blocking", "path_escape"]) {
      const d = denyExplanation(code);
      expect(d).not.toBeNull();
      expect(d!.what).toBeTruthy();
      expect(d!.why).toBeTruthy();
      expect(d!.next).toBeTruthy();
    }
  });

  it("the blast_radius explanation names the fix and the split command", () => {
    const d = denyExplanation("blast_radius")!;
    expect(d.next).toContain("max_files");
    expect(d.command).toContain("split-task");
  });

  it("review_open_blocking points at disposition and accept-findings", () => {
    const d = denyExplanation("review_open_blocking")!;
    expect(d.command).toContain("--accept-findings");
  });

  it("fails open to a generic explanation for an unknown code, never vanishing", () => {
    const d = denyExplanation("some_future_code")!;
    expect(d.label).toBe("system deny");
    expect(d.what).toBeTruthy();
  });
});

describe("isSystemDeny", () => {
  it("is true for a known code, false otherwise", () => {
    expect(isSystemDeny("secret_sweep")).toBe(true);
    expect(isSystemDeny("")).toBe(false);
    expect(isSystemDeny(undefined)).toBe(false);
  });
});

describe("DENY_CODES coverage", () => {
  it("has an entry for every deny code the backend defines", () => {
    // domain.DenyCode.Valid(): secret_sweep, pii, path_escape, host_exec,
    // blob_digest_mismatch, trust_deny, review_open_blocking, blast_radius.
    for (const code of [
      "secret_sweep",
      "pii",
      "path_escape",
      "host_exec",
      "blob_digest_mismatch",
      "trust_deny",
      "review_open_blocking",
      "blast_radius",
    ]) {
      expect(DENY_CODES[code], `missing ${code}`).toBeTruthy();
    }
  });
});

describe("denyFromError", () => {
  it("extracts a deny code from a raw daemon error", () => {
    const d = denyFromError("workflow: approval blocked: system deny review_open_blocking (open critical finding)");
    expect(d).not.toBeNull();
    expect(d!.label).toBe("blocking finding open");
    expect(d!.command).toContain("--accept-findings");
  });

  it("extracts blast_radius from its message", () => {
    const d = denyFromError("system deny blast_radius: touches 26 files, over execute.max_files");
    expect(d!.label).toBe("too many files");
  });

  it("returns null for an error with no deny code", () => {
    expect(denyFromError("network error: connection refused")).toBeNull();
  });
});
