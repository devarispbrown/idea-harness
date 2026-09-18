import { describe, expect, it, vi } from "vitest";
import { loadSettingsOnce, resolveInitialProvider } from "./settingsLoad";

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
describe("loadSettingsOnce", () => {
  it("fetches the providers view once and that provider's models exactly once", async () => {
    const fetchProviders = vi.fn().mockResolvedValue({ default_model: "anthropic/claude-x" });
    const fetchModels = vi.fn().mockResolvedValue(undefined);
    await loadSettingsOnce(fetchProviders, fetchModels);
    expect(fetchProviders).toHaveBeenCalledTimes(1);
    expect(fetchModels).toHaveBeenCalledTimes(1);
    expect(fetchModels).toHaveBeenCalledWith("anthropic");
  });

  it("falls back to openai's models when no default model is set", async () => {
    const fetchProviders = vi.fn().mockResolvedValue({ default_model: "" });
    const fetchModels = vi.fn().mockResolvedValue(undefined);
    await loadSettingsOnce(fetchProviders, fetchModels);
    expect(fetchModels).toHaveBeenCalledWith("openai");
  });

  it("running it twice makes exactly two independent rounds, never a growing chain", async () => {
    const fetchProviders = vi.fn().mockResolvedValue({ default_model: "" });
    const fetchModels = vi.fn().mockResolvedValue(undefined);
    await loadSettingsOnce(fetchProviders, fetchModels);
    await loadSettingsOnce(fetchProviders, fetchModels);
    expect(fetchProviders).toHaveBeenCalledTimes(2);
    expect(fetchModels).toHaveBeenCalledTimes(2);
  });
});
