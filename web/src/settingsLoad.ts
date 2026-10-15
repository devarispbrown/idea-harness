// Pure helpers extracted from Settings' mount-time load sequence (C1), so the
// sequencing itself -- "fetch the providers view once, then that provider's
// models once" -- is testable without React state or a DOM.
//
// The reload loop this fixes: `loadLiveModels` closed over the `view` state
// (to look up the just-fetched provider's readiness for its status message),
// so `setView` inside `load` gave `loadLiveModels` a new identity on every
// call. `load` depended on `loadLiveModels`, so IT got a new identity too,
// which re-ran the mount effect that calls `load` -- forever. Each cycle
// re-fetched /providers and made a live /models call, and reset whatever the
// user was typing, the selected provider, gitCommit, the error text, and the
// Saved message along with it.
//
// The fix has two parts: Settings.tsx now reads "the just-fetched view" via a
// ref instead of a `view` dependency (so `loadLiveModels`'s identity is
// stable), and `load` runs on mount/explicit-refresh only while live models
// load in their own effect keyed on the selected provider. Neither of the
// functions below closes over any component state at all, which is what
// makes "runs exactly once per call, never a growing chain" provable here:
// calling either of them again is a fresh, independent round, never a
// continuation of the last one's own result.

export interface ProvidersViewLike {
  default_model: string;
}

// resolveInitialProvider picks which provider's models to preload once the
// providers view has loaded: the default model's provider; with no default,
// the first provider that is actually ready; "openai" only when none is.
// Settings.tsx's `load` uses this same rule.
//
// "openai" unconditionally used to greet a fresh install with an amber
// "OPENAI_API_KEY is not set…" nobody had asked about, while a working local
// provider sat one line further down.
export function resolveInitialProvider(
  defaultModel: string,
  providers: { name: string; ready: boolean }[] = [],
): string {
  if (defaultModel?.includes("/")) return defaultModel.split("/")[0];
  return providers.find((p) => p.ready)?.name ?? "openai";
}
