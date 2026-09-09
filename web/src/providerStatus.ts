// Provider status mapping, extracted from Settings so it can be unit tested
// without a DOM — the same reason statusChips.ts exists.
//
// `ready` folds three separate facts together: the provider needs no key, the
// key is missing, and **the daemon refused this entry** (a built-in preset whose
// base_url was overridden, which would send that preset's api_key_env to
// somewhere else). Rendering all of them as "missing key" is not a cosmetic
// slip: it tells an operator to go set a key at the exact moment they should be
// looking at base_url, and that moment is a credential-exfiltration attempt.
// docs/security.md names this surface as where the reason is shown.

export interface ProviderStatusInput {
  ready: boolean;
  key_set: boolean;
  configured: boolean;
  key_env: string;
  problem?: string;
}

export type ProviderState = "ready" | "refused" | "key-missing" | "not-configured";

// `problem` is checked first and independently of `ready`: a refused entry is
// not unusable because of its key, and its fix is in harness.yaml.
export function providerState(p: ProviderStatusInput): ProviderState {
  if (p.problem) return "refused";
  if (p.ready) return "ready";
  if (p.configured) return "key-missing";
  return "not-configured";
}

export function providerStatusLabel(p: ProviderStatusInput): string {
  switch (providerState(p)) {
    case "ready":
      return "ready";
    case "refused":
      return "refused";
    case "key-missing":
      return "missing key";
    default:
      return "not configured";
  }
}

export function providerOptionSuffix(p: ProviderStatusInput): string {
  switch (providerState(p)) {
    case "ready":
      return "";
    case "refused":
      return " (refused)";
    case "key-missing":
      return " (key missing)";
    default:
      return " (not configured)";
  }
}

// The line under the provider name. For a refused entry this is the daemon's own
// reason — the one thing docs/security.md promises is shown here.
export function providerDetail(p: ProviderStatusInput): string {
  switch (providerState(p)) {
    case "refused":
      return p.problem ?? "";
    case "key-missing":
      return `${p.key_env} is not set in the daemon's environment`;
    default:
      return "";
  }
}
