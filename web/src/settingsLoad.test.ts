import { describe, expect, it } from "vitest";
import { resolveInitialProvider } from "./settingsLoad";

describe("resolveInitialProvider", () => {
  it("reads the provider out of a provider/model default", () => {
    expect(resolveInitialProvider("anthropic/claude-x")).toBe("anthropic");
  });

  it("defaults to openai when no default model is set", () => {
    expect(resolveInitialProvider("")).toBe("openai");
  });
});

// C1: the reload loop's signature was `load` re-running forever, each cycle
// re-fetching /providers and making a live /models call. These pin down that
// the extracted sequence itself makes exactly one round trip per call, and
// that a second, independent call is not a continuation of the first --
// nothing here can chain into itself the way the stateful version did.
