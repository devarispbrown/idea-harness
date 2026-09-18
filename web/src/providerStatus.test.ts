import { describe, expect, it } from "vitest";
import {
  modelsLoadedMessage,
  providerDetail,
  providerDotState,
  providerOptionSuffix,
  providerReadinessWarning,
  providerState,
  providerStatusLabel,
} from "./providerStatus";

const refused = {
  ready: false,
  key_set: true,
  configured: true,
  key_env: "OPENAI_API_KEY",
  problem: 'provider "openai" is a built-in preset; its base_url may not be overridden',
};

const noKey = { ready: false, key_set: false, configured: true, key_env: "ANTHROPIC_API_KEY" };
const keyless = { ready: true, key_set: true, configured: false, key_env: "" };
const unconfigured = { ready: false, key_set: false, configured: false, key_env: "GOOGLE_API_KEY" };

describe("providerState", () => {
  it("calls a refused entry refused, not missing a key it has", () => {
    expect(providerState(refused)).toBe("refused");
  });

  it("lets a problem outrank ready", () => {
    expect(providerState({ ...refused, ready: true })).toBe("refused");
  });

  it("still reports a genuinely missing key", () => {
    expect(providerState(noKey)).toBe("key-missing");
  });

  it("treats a keyless local provider as ready", () => {
    expect(providerState(keyless)).toBe("ready");
  });

  it("distinguishes an unconfigured preset from one missing its key", () => {
    expect(providerState(unconfigured)).toBe("not-configured");
  });
});

describe("labels", () => {
  it("never calls a refused entry a key problem", () => {
    expect(providerStatusLabel(refused)).toBe("refused");
    expect(providerOptionSuffix(refused)).toBe(" (refused)");
  });

  it("leaves the missing-key wording alone", () => {
    expect(providerStatusLabel(noKey)).toBe("missing key");
    expect(providerOptionSuffix(noKey)).toBe(" (key missing)");
  });

  it("adds no suffix for a ready provider", () => {
    expect(providerOptionSuffix(keyless)).toBe("");
  });
});

describe("providerDetail", () => {
  // docs/security.md's threat-model row promises the refusal reason is shown
  // against the provider here. This is that promise as a test.
  it("shows the daemon's reason for a refused entry", () => {
    expect(providerDetail(refused)).toContain("base_url may not be overridden");
  });

  it("names the variable for a missing key", () => {
    expect(providerDetail(noKey)).toContain("ANTHROPIC_API_KEY");
  });

  it("says nothing when there is nothing wrong", () => {
    expect(providerDetail(keyless)).toBe("");
  });
});

describe("providerReadinessWarning", () => {
  it("warns nothing for a ready provider", () => {
    expect(providerReadinessWarning(keyless)).toBeUndefined();
  });

  it("names the key variable and the restart step for a missing key", () => {
    const w = providerReadinessWarning(noKey);
    expect(w).toContain("ANTHROPIC_API_KEY");
    expect(w).toContain("idea daemon stop");
  });

  it("names the key variable and the restart step for an unconfigured preset", () => {
    const w = providerReadinessWarning(unconfigured);
    expect(w).toContain("GOOGLE_API_KEY");
    expect(w).toContain("idea daemon stop");
  });

  it("carries the daemon's own reason for a refused entry, not a key nudge", () => {
    const w = providerReadinessWarning(refused);
    expect(w).toContain("base_url may not be overridden");
    expect(w).not.toContain("is not set");
  });
});

describe("modelsLoadedMessage", () => {
  it("reports the count for a ready provider", () => {
    expect(modelsLoadedMessage({ ...keyless, name: "ollama" }, 7)).toBe(
      "Loaded 7 models from ollama.",
    );
  });

  it("uses singular for exactly one model", () => {
    expect(modelsLoadedMessage({ ...keyless, name: "ollama" }, 1)).toBe(
      "Loaded 1 model from ollama.",
    );
  });

  it("names the key variable instead of a bogus zero-model count when not ready", () => {
    const msg = modelsLoadedMessage({ ...noKey, name: "anthropic" }, 0);
    expect(msg).toContain("ANTHROPIC_API_KEY");
    expect(msg).not.toContain("Loaded 0 models");
  });
});

describe("providerDotState", () => {
  it("is ready only when the provider is actually ready", () => {
    expect(providerDotState(keyless)).toBe("ready");
  });

  it("is warn for a missing key, not the green a bare 'not ready' used to get", () => {
    expect(providerDotState(noKey)).toBe("warn");
  });

  it("is warn for a refused entry", () => {
    expect(providerDotState(refused)).toBe("warn");
  });

  it("is warn for an unconfigured preset", () => {
    expect(providerDotState(unconfigured)).toBe("warn");
  });
});
