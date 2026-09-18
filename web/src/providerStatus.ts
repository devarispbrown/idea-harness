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

// providerReadinessWarning is what Settings shows when the user saves a model
// or an executor pointed at a provider that is not actually ready (F4). It
// says which variable to set — the F1 pre-flight check names the same
// variable, so a user who sees this before running and F1's failure after
// running are reading the same fact — and that a daemon already running
// needs restarting to pick up an env change made after it started (`idea
// daemon stop`; the supervisor brings it back).
export function providerReadinessWarning(p: ProviderStatusInput): string | undefined {
  switch (providerState(p)) {
    case "ready":
      return undefined;
    case "refused":
      return p.problem
        ? `${p.problem} Fix it in harness.yaml, then run \`idea daemon stop\` to pick it up.`
        : undefined;
    default:
      // key-missing and not-configured are both fixed the same way: set the
      // key, then restart the daemon so it re-reads the environment.
      return `${p.key_env || "its API key"} is not set in the daemon's environment or the project's .env; set it and run \`idea daemon stop\`.`;
  }
}

// modelsLoadedMessage is the Settings model-picker status line. A provider
// that cannot actually be called should never be told "loaded 0 models" —
// that reads as a transient glitch worth retrying, when the fix is
// configuration (I3).
export function modelsLoadedMessage(
  p: ProviderStatusInput & { name: string },
  count: number,
): string {
  if (providerState(p) !== "ready") {
    return providerReadinessWarning(p) ?? `${p.name} is not ready.`;
  }
  return `Loaded ${count} model${count === 1 ? "" : "s"} from ${p.name}.`;
}

// providerDotState is what the sidebar's provider dot (PhaseNav's
// `.pn-model-dot`) renders as. Only "ready" is a clean bill of health — a
// refused entry, a missing key, and an unconfigured preset all mean "this
// will not actually answer a call," so all three read the same amber, not
// the green a merely-absent-key case used to get (F4). App.tsx renders the
// dot; this is the derivation it renders from.
export type ProviderDotState = "ready" | "warn";

export function providerDotState(p: ProviderStatusInput): ProviderDotState {
  return providerState(p) === "ready" ? "ready" : "warn";
}
