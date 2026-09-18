import { describe, expect, it } from "vitest";
import {
  executorImagePlaceholder,
  executorReady,
  executorStatusLines,
  type ExecutorStatusInput,
} from "./executorStatus";

const ready: ExecutorStatusInput = {
  image: "ghcr.io/org/agent:latest",
  image_present: true,
  key_env: "ANTHROPIC_API_KEY",
  key_set: true,
  problem: "",
};

describe("executorStatusLines", () => {
  it("says the image is pulled and the key is set when ready", () => {
    const lines = executorStatusLines(ready);
    expect(lines.imageLine).toEqual({ text: "ghcr.io/org/agent:latest is pulled.", ok: true });
    expect(lines.keyLine).toEqual({ text: "ANTHROPIC_API_KEY is set.", ok: true });
    expect(lines.problemLine).toBeUndefined();
  });

  it("names the setup command when the image is not pulled", () => {
    const lines = executorStatusLines({ ...ready, image_present: false });
    expect(lines.imageLine.ok).toBe(false);
    expect(lines.imageLine.text).toContain("idea executors setup");
  });

  it("falls back to naming the default image when none is configured", () => {
    const lines = executorStatusLines({ ...ready, image: "", image_present: false });
    expect(lines.imageLine.text).toContain("the pinned default image");
  });

  it("reports a missing key against the daemon's environment and .env", () => {
    const lines = executorStatusLines({ ...ready, key_set: false });
    expect(lines.keyLine?.ok).toBe(false);
    expect(lines.keyLine?.text).toContain("ANTHROPIC_API_KEY");
    expect(lines.keyLine?.text).toContain(".env");
  });

  it("omits the key line for a keyless executor kind", () => {
    const lines = executorStatusLines({ ...ready, key_env: "" });
    expect(lines.keyLine).toBeUndefined();
  });

  it("surfaces the problem line verbatim when present", () => {
    const lines = executorStatusLines({ ...ready, problem: "image lacks a hermes CLI" });
    expect(lines.problemLine).toBe("image lacks a hermes CLI");
  });
});

describe("executorReady", () => {
  it("is ready exactly when problem is empty", () => {
    expect(executorReady(ready)).toBe(true);
    expect(executorReady({ ...ready, problem: "no CLI" })).toBe(false);
  });

  // Mutation guard: readiness must track `problem`, not image/key state —
  // an executor can have image_present=false and key_set=false and still be
  // the thing the daemon calls "ready" if problem is empty for some other
  // reason the UI doesn't need to re-derive.
  it("does not derive readiness from image or key state directly", () => {
    expect(executorReady({ ...ready, image_present: false, key_set: false, problem: "" })).toBe(
      true,
    );
  });
});

describe("executorImagePlaceholder", () => {
  it("names the default image when one exists", () => {
    expect(executorImagePlaceholder("ghcr.io/org/hermes:latest")).toContain(
      "ghcr.io/org/hermes:latest",
    );
  });

  it("falls back to a generic example when there is no default", () => {
    expect(executorImagePlaceholder(undefined)).toContain("e.g.");
  });
});
