import { describe, expect, it } from "vitest";
import {
  providerDetail,
  providerOptionSuffix,
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
