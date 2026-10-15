import { useCallback, useEffect, useRef, useState } from "react";
import { del, get, put } from "./api";
import {
  providerDetail,
  providerOptionSuffix,
  providerReadinessWarning,
  providerState,
  providerListLabel,
  modelsLoadedMessage,
} from "./providerStatus";
import { chipsToShow, orderModels } from "./modelCatalog";
import { resolveInitialProvider } from "./settingsLoad";
import IntegrationsPanel from "./IntegrationsPanel";
import NotificationsPanel from "./NotificationsPanel";
import RemoteAccessPanel from "./RemoteAccessPanel";
import ExecutorSettings from "./settings/ExecutorSettings";
import BudgetSettings from "./settings/BudgetSettings";

interface Provider {
  name: string;
  label: string;
  type: string;
  base_url: string;
  key_env: string;
  ready: boolean;
  // Narrower than ready, which also folds in whether the entry was refused.
  key_set: boolean;
  // The daemon's reason for refusing this entry, when it did.
  problem?: string;
  configured: boolean;
  is_default: boolean;
  models: string[];
}

interface ProvidersView {
  default_model: string;
  git_commit?: boolean;
  // Wave 3 (D3/C4 review changes): the backend for these two is landing
  // alongside this UI, so both are read with `??`/optional chaining and
  // default to their safe "off" state when a daemon predates them —
  // git_commit itself already followed this rule.
  git_allow_default_branch?: boolean;
  git_branch_per_initiative?: boolean;
  // "Already shipped" per the wave-3 contract, but still optional here: a
  // daemon whose harness.yaml predates review.rigor sends no field at all.
  review_rigor?: string;
  demo?: boolean;
  providers: Provider[];
}

// SecretAllowlistEntry mirrors docs/openapi.yaml's SecretAllowlistEntry
// (review changes #1): one human "not a secret" override, listed in
// Settings so it can be seen and revoked instead of living forever as an
// invisible standing exception.
interface SecretAllowlistEntry {
  id: string;
  // M8, review fix: the backend actually sends `fingerprint` -- the full
  // per-project HMAC, not a pre-truncated `fingerprint_prefix` as
  // docs/openapi.yaml documents the eventual contract to be. Read the field
  // that is really there and truncate for display client-side below, rather
  // than reading a field that has never once been sent (rendered as
  // "(…)" ). May be superseded once the secret-masking work elsewhere
  // makes the backend send a real prefix instead.
  fingerprint: string;
  kind: string;
  path: string;
  actor: string;
  created_at: string;
}

// allowlistFingerprintPrefix (M8, review fix) is the short, non-identifying
// slice of the full HMAC shown next to a "not a secret" exemption -- enough
// to recognize which one it is, never the full value.
function allowlistFingerprintPrefix(fingerprint: string): string {
  return (fingerprint || "").slice(0, 12);
}

const REVIEW_RIGOR_OPTIONS: { value: string; label: string; explanation: string }[] = [
  {
    value: "auto",
    label: "Auto",
    explanation: "Calibrates to the project's size and the size of the change — the default.",
  },
  { value: "quick", label: "Quick", explanation: "One pass, up to 5 findings. For a small, low-risk change." },
  { value: "standard", label: "Standard", explanation: "Up to 10 findings. The default rigor for most changes." },
  { value: "thorough", label: "Thorough", explanation: "Up to 20 findings. For a large or sensitive change." },
];

// api.ts throws an Error whose .message is already the server's plain `error`
// string (no JSON to parse here) — this just narrows the catch(e: unknown)
// down to that .message for the one inline error this page shows next to a
// field rather than in the page-level banner (C3).
function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

const PRESETS = [
  { key: "openai", label: "OpenAI", type: "openai", base_url: "https://api.openai.com/v1", key_env: "OPENAI_API_KEY" },
  { key: "anthropic", label: "Anthropic", type: "anthropic", base_url: "https://api.anthropic.com/v1", key_env: "ANTHROPIC_API_KEY" },
  { key: "gemini", label: "Google Gemini", type: "gemini", base_url: "https://generativelanguage.googleapis.com/v1beta", key_env: "GOOGLE_API_KEY" },
  { key: "deepseek", label: "DeepSeek", type: "openai", base_url: "https://api.deepseek.com/v1", key_env: "DEEPSEEK_API_KEY" },
  { key: "openrouter", label: "OpenRouter", type: "openai", base_url: "https://openrouter.ai/api/v1", key_env: "OPENROUTER_API_KEY" },
  { key: "ollama", label: "Ollama (local)", type: "openai", base_url: "http://localhost:11434/v1", key_env: "" },
];

export default function Settings({ projectId }: { projectId: string }) {
  const [view, setView] = useState<ProvidersView | null>(null);
  const [demo, setDemo] = useState(false);
  const [defaultModel, setDefaultModel] = useState("");
  // Empty until the providers view has loaded and chosen the default
  // provider. Starting at "openai" fetched openai's models before the view
  // existed: no key state to judge it by, so "Loaded 0 models" showed in green,
  // and its slow live listing could land after the real provider's.
  const [selectedProvider, setSelectedProvider] = useState("");
  const selectedProviderRef = useRef("");
  selectedProviderRef.current = selectedProvider;
  const [liveModels, setLiveModels] = useState<string[]>([]);
  const [loadingModels, setLoadingModels] = useState(false);
  const [modelQuery, setModelQuery] = useState("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  // A readiness nudge shown alongside a successful save, not instead of it
  // (F4): saving a model/executor pointed at an unready provider still
  // succeeds — the daemon just can't call it yet.
  const [warning, setWarning] = useState("");
  // What loading a provider's models found: the count, or why that provider
  // cannot be called. Shown inside the Model card it is about. It used to go
  // to the page-level lines at the very bottom, so opening Settings greeted
  // you with "OPENAI_API_KEY is not set…" under Integrations, about a provider
  // you had not touched.
  const [modelNotice, setModelNotice] = useState<{ text: string; warn: boolean } | null>(null);

  // add-provider form state
  const [preset, setPreset] = useState("openai");
  const [baseUrl, setBaseUrl] = useState("");
  const [keyEnv, setKeyEnv] = useState("");

  // git commit on apply toggle, plus the two wave-3 companions (review
  // changes #2/D3): the default-branch guard and branch-per-initiative.
  const [gitCommit, setGitCommit] = useState(false);
  const [gitAllowDefaultBranch, setGitAllowDefaultBranch] = useState(false);
  const [gitBranchPerInitiative, setGitBranchPerInitiative] = useState(false);

  // Review rigor (B1/D1). Falls back to "auto" — the daemon's own default —
  // when the field is absent, never to an empty select with nothing chosen.
  const [reviewRigor, setReviewRigor] = useState("auto");

  // Secret exemptions (review changes #1). Project-scoped, unlike everything
  // else on this page's "Apply"/Git sections which is per-model config only
  // in spirit — the allowlist itself has always been project-wide.
  const [allowlist, setAllowlist] = useState<SecretAllowlistEntry[]>([]);
  const [allowlistError, setAllowlistError] = useState("");

  // C1: `viewRef` mirrors `view` state so loadLiveModels can read the latest
  // providers view (to look up the provider's readiness for its status
  // message) WITHOUT depending on `view` as a useCallback dependency. That
  // dependency was the reload loop: loadLiveModels closed over `view` ->
  // setView inside `load` gave loadLiveModels a new identity every render ->
  // `load` (which depended on loadLiveModels) got a new identity too -> the
  // mount effect below, keyed on `load`, re-ran `load()` -> forever. Each
  // cycle re-fetched /providers and made a live /models call, and reset
  // whatever the user was typing, the selected provider, gitCommit, the
  // error text, and the Saved message. A ref can't do that: reading/writing
  // `.current` never changes any callback's identity.
  const viewRef = useRef<ProvidersView | null>(null);

  // loadLiveModels fetches one provider's live listing, orders it
  // curated-first (I2), and reports either the count or — for a provider
  // that cannot actually be called — why not, instead of "Loaded 0 models"
  // (I3/F4). Depends on projectId only, so its identity is stable across the
  // component's lifetime.
  const loadLiveModels = useCallback(
    async (provider: string) => {
      setLoadingModels(true);
      setError("");
      setModelNotice(null);
      try {
        const v = await get<{ provider: string; models: string[] }>(
          `/projects/${projectId}/models?provider=${provider}`,
        );
        // The user may have switched provider while this was in flight; a late
        // answer for the old one must not replace the new one's models.
        if (provider !== selectedProviderRef.current) return;
        const ordered = orderModels(provider, v.models ?? []);
        setLiveModels(ordered.map((m) => `${provider}/${m}`));
        const entry = viewRef.current?.providers.find((pr) => pr.name === provider);
        // A provider that is not ready gets its reason (missing key,
        // refused, ...) — that is a warning about the provider, not a
        // success, and used to render in the green "Saved" line because
        // modelsLoadedMessage's not-ready text was pushed through
        // `setSaved` here. Route it to `warning` (amber) instead.
        if (entry && providerState(entry) !== "ready") {
          setModelNotice({ text: modelsLoadedMessage(entry, ordered.length), warn: true });
        } else {
          setModelNotice({
            text: entry
              ? modelsLoadedMessage(entry, ordered.length)
              : `Loaded ${ordered.length} models from ${provider}.`,
            warn: false,
          });
        }
      } catch (e) {
        setError(errorMessage(e));
      } finally {
        setLoadingModels(false);
      }
    },
    [projectId],
  );

  // load fetches the providers view only -- it no longer also fetches live
  // models (that used to be the other half of the loop: `load` awaited
  // loadLiveModels, so `load`'s own identity was hostage to loadLiveModels's).
  // Depends on projectId only, so it runs on mount and on an explicit
  // refresh (addProvider/saveDefault/toggleGitCommit calling it), never as a
  // side effect of its own prior run.
  const load = useCallback(async () => {
    try {
      const v = await get<ProvidersView>(`/projects/${projectId}/providers`);
      setView(v);
      viewRef.current = v;
      setDemo(v.demo ?? false);
      setDefaultModel(v.default_model || "");
      setGitCommit(v.git_commit ?? false);
      setGitAllowDefaultBranch(v.git_allow_default_branch ?? false);
      setGitBranchPerInitiative(v.git_branch_per_initiative ?? false);
      setReviewRigor(v.review_rigor || "auto");
      // I1: this sets the provider whose models the effect below preloads;
      // Setting the provider starts the model fetch, in its own effect; load
      // itself never depends on the view, which is what looped.
      setSelectedProvider(resolveInitialProvider(v.default_model || "", v.providers ?? []));
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [projectId]);

  const loadAllowlist = useCallback(async () => {
    try {
      const list = await get<SecretAllowlistEntry[]>(`/projects/${projectId}/secret-allowlist`);
      setAllowlist(list ?? []);
    } catch {
      // Pre-wave-3 daemon, or a transient error -- the empty state below
      // already reads correctly ("no exemptions") for either case.
    }
  }, [projectId]);

  // Mount / explicit-refresh only -- see the comment on `load` above.
  useEffect(() => {
    load();
  }, [load]);

  useEffect(() => {
    loadAllowlist();
  }, [loadAllowlist]);

  // C1: live models load in their own effect, keyed on the selected
  // provider -- not folded into `load`. `load` sets `selectedProvider` from
  // the fetched default model, which fires this effect exactly once for the
  // right provider; switching the dropdown (selectProvider) or clicking
  // "Reload models" fires it again the same way, without touching `load`.
  useEffect(() => {
    if (!selectedProvider) return;
    loadLiveModels(selectedProvider);
  }, [selectedProvider, loadLiveModels]);

  const allModels = () => {
    const seen = new Set<string>();
    const out: string[] = [];
    if (defaultModel) {
      out.push(defaultModel);
      seen.add(defaultModel);
    }
    for (const p of view?.providers ?? []) {
      if (!p.ready) continue;
      for (const m of p.models ?? []) {
        const spec = `${p.name}/${m}`;
        if (!seen.has(spec)) {
          seen.add(spec);
          out.push(spec);
        }
      }
    }
    return out;
  };

  const saveDefault = async () => {
    setError("");
    setSaved("");
    setWarning("");
    try {
      await put(`/projects/${projectId}/providers`, { default_model: defaultModel });
      setSaved("Saved — will apply to the next initiative.");
      // F4: the save still succeeds — the daemon just records the spec — but
      // if the provider it names cannot actually be called yet, say so here
      // rather than let the next run fail with no warning beforehand.
      const providerName = defaultModel.includes("/") ? defaultModel.split("/")[0] : "";
      const entry = view?.providers.find((p) => p.name === providerName);
      if (entry) setWarning(providerReadinessWarning(entry) ?? "");
      await load();
    } catch (e) {
      setError(errorMessage(e));
    }
  };

  const applyPreset = (key: string) => {
    setPreset(key);
    const p = PRESETS.find((x) => x.key === key);
    if (p) {
      setBaseUrl(p.base_url);
      setKeyEnv(p.key_env);
    }
  };

  const addProvider = async () => {
    setError("");
    setSaved("");
    try {
      await put(`/projects/${projectId}/providers`, {
        providers: {
          [preset]: {
            type: PRESETS.find((x) => x.key === preset)?.type ?? "openai",
            base_url: baseUrl,
            api_key_env: keyEnv,
          },
        },
      });
      setSaved(`Provider "${preset}" configured.`);
      await load();
    } catch (e) {
      setError(errorMessage(e));
    }
  };

  const toggleGitCommit = async (checked: boolean) => {
    setGitCommit(checked);
    setError("");
    setSaved("");
    try {
      await put(`/projects/${projectId}/providers`, { git_commit: checked });
      setSaved(`git commit on apply ${checked ? "enabled" : "disabled"}.`);
    } catch (e) {
      setError(errorMessage(e));
      setGitCommit(!checked);
    }
  };

  // Wave 3 / review changes #2: the default-branch guard and
  // branch-per-initiative, each its own toggle so either can be turned on
  // without the other. Both PUT the single field that changed, same pattern
  // as toggleGitCommit — an optimistic flip, reverted on a refusal.
  const toggleGitAllowDefaultBranch = async (checked: boolean) => {
    setGitAllowDefaultBranch(checked);
    setError("");
    setSaved("");
    try {
      await put(`/projects/${projectId}/providers`, { git_allow_default_branch: checked });
      setSaved(`Committing on the default branch ${checked ? "allowed" : "refused"}.`);
    } catch (e) {
      setError(errorMessage(e));
      setGitAllowDefaultBranch(!checked);
    }
  };

  const toggleGitBranchPerInitiative = async (checked: boolean) => {
    setGitBranchPerInitiative(checked);
    setError("");
    setSaved("");
    try {
      await put(`/projects/${projectId}/providers`, { git_branch_per_initiative: checked });
      setSaved(`Branch per initiative ${checked ? "enabled" : "disabled"}.`);
    } catch (e) {
      setError(errorMessage(e));
      setGitBranchPerInitiative(!checked);
    }
  };

  const saveReviewRigor = async (value: string) => {
    const previous = reviewRigor;
    setReviewRigor(value);
    setError("");
    setSaved("");
    try {
      await put(`/projects/${projectId}/providers`, { review_rigor: value });
      setSaved(`Review rigor set to ${value}.`);
    } catch (e) {
      setError(errorMessage(e));
      setReviewRigor(previous);
    }
  };

  // revokeAllowlistEntry (review changes #1): removes one "not a secret"
  // override, so the sweep denies that fingerprint again from then on.
  // Optimistic removal, restored on a refusal — same shape as the toggles
  // above, adapted to a list rather than a single boolean.
  const revokeAllowlistEntry = async (id: string) => {
    setAllowlistError("");
    const previous = allowlist;
    setAllowlist((list) => list.filter((e) => e.id !== id));
    try {
      await del(`/projects/${projectId}/secret-allowlist/${id}`);
    } catch (e) {
      setAllowlistError(errorMessage(e));
      setAllowlist(previous);
    }
  };

  const models = Array.from(new Set([...allModels(), ...liveModels]));

  const selectProvider = (provider: string) => {
    // Clears the stale list immediately; the effect above (keyed on
    // selectedProvider) does the actual fetch, so this doesn't also call
    // loadLiveModels directly -- that would fire it twice for one click.
    setSelectedProvider(provider);
    setLiveModels([]);
  };

  // I2/I3: liveModels is already curated-first and chat-only (orderModels,
  // in loadLiveModels above); this just bounds how many render at once and
  // lets a typed query reach past that bound (I "Bound OpenRouter's 446
  // chips").
  const visibleModels = chipsToShow(liveModels, modelQuery);

  return (
    <div className="settings-page">
      {/* No banner of its own: the app shell's demo banner is on this page
          too, and the two said it twice. */}
      <section className="card">
        <h2>Model</h2>

        <label className="field-label" htmlFor="provider-select">
          Provider
        </label>
        <select
          id="provider-select"
          className="text-input"
          value={selectedProvider}
          onChange={(e) => selectProvider(e.target.value)}
        >
          {(view?.providers ?? []).map((p) => (
            <option key={p.name} value={p.name}>
              {p.label}
              {providerOptionSuffix(p)}
            </option>
          ))}
        </select>

        <label className="field-label" htmlFor="model-query">
          Model
        </label>
        <input
          id="model-query"
          className="text-input"
          value={modelQuery}
          placeholder={`Filter ${selectedProvider} models…`}
          onChange={(e) => setModelQuery(e.target.value)}
        />
        {visibleModels.length > 0 && (
          <div className="model-grid">
            {visibleModels.map((m) => (
              <button
                key={m}
                className={`model-chip ${m === defaultModel ? "selected" : ""}`}
                onClick={() => setDefaultModel(m)}
                title={m}
              >
                {m.replace(`${selectedProvider}/`, "")}
              </button>
            ))}
          </div>
        )}

        <label className="field-label" htmlFor="default-model">
          Selected model
        </label>
        <input
          id="default-model"
          className="text-input"
          list="model-options"
          value={defaultModel}
          placeholder="provider/model — or pick from the list above"
          onChange={(e) => setDefaultModel(e.target.value)}
        />
        <datalist id="model-options">
          {models.map((m) => (
            <option key={m} value={m} />
          ))}
        </datalist>

        <div className="row">
          <button onClick={saveDefault}>Save model</button>
          <button
            className="ghost"
            onClick={() => loadLiveModels(selectedProvider)}
            disabled={loadingModels}
          >
            {loadingModels ? "Loading…" : "Reload models"}
          </button>
        </div>
        <p className="muted">
          Pick a provider, then a model from the live list — or type any{" "}
          <code>provider/model</code> id your provider supports. Leave blank to auto-select.
        </p>
        {modelNotice && (
          <p className={modelNotice.warn ? "warn-text" : "muted"} role="status">
            {modelNotice.text}
          </p>
        )}
      </section>

      <section className="card">
        <h2>Providers</h2>
        <ul className="list">
          {(view?.providers ?? []).map((p) => (
            <li key={p.name} className="provider-row">
              <div>
                <strong>{p.label}</strong>{" "}
                {p.is_default && <span className="badge ok">default</span>}
                <span className={`dot ${providerState(p) === "ready" ? "ready" : ""}`} />
                <div className="muted">
                  {p.type} · {p.base_url || "default endpoint"}
                  {p.key_env ? ` · key: ${p.key_env}` : " · no key (local)"}
                </div>
                {providerDetail(p) && (
                  <div className={providerState(p) === "refused" ? "error" : "muted"}>
                    {providerDetail(p)}
                  </div>
                )}
              </div>
              <span className="muted">{providerListLabel(p, demo)}</span>
            </li>
          ))}
        </ul>
      </section>

      <section className="card">
        <h2>Add provider</h2>
        <label className="field-label" htmlFor="add-provider-preset">Provider</label>
        <select
          id="add-provider-preset"
          className="text-input"
          value={preset}
          onChange={(e) => applyPreset(e.target.value)}
        >
          {PRESETS.map((p) => (
            <option key={p.key} value={p.key}>
              {p.label}
            </option>
          ))}
        </select>
        <label className="field-label" htmlFor="add-provider-base-url">Base URL</label>
        <input
          id="add-provider-base-url"
          className="text-input"
          value={baseUrl}
          placeholder="endpoint URL"
          onChange={(e) => setBaseUrl(e.target.value)}
        />
        <label className="field-label" htmlFor="add-provider-key-env">API key environment variable</label>
        <input
          id="add-provider-key-env"
          className="text-input"
          value={keyEnv}
          placeholder="e.g. OPENAI_API_KEY (leave empty for local)"
          onChange={(e) => setKeyEnv(e.target.value)}
        />
        <p className="muted">
          API keys are read from environment variables, never stored inline.
        </p>
        <div className="row">
          <button onClick={addProvider}>Add provider</button>
        </div>
      </section>

      <ExecutorSettings projectId={projectId} />

      <BudgetSettings projectId={projectId} />

      {/* Git settings were hidden — wave 3 gives git_commit its companions
          (review changes #2/D3): the default-branch guard and
          branch-per-initiative, each opt-in and each explained plainly. */}
      <section className="card w3-git">
        <h2>Git</h2>
        <label className="w3-toggle-row">
          <input
            type="checkbox"
            checked={gitCommit}
            onChange={(e) => toggleGitCommit(e.target.checked)}
          />
          Commit on apply
        </label>
        <p className="muted">
          Off by default. When on, each applied change set is committed automatically — this is
          opt-in, never assumed.
        </p>

        <label className="w3-toggle-row">
          <input
            type="checkbox"
            checked={gitAllowDefaultBranch}
            onChange={(e) => toggleGitAllowDefaultBranch(e.target.checked)}
          />
          Allow committing on the default branch
        </label>
        <p className="muted">
          Off by default. Committing on your default branch (e.g. <code>main</code>) is refused
          unless you allow it here — the guard exists so an agent never lands a commit straight
          onto the branch you ship from.
        </p>

        <label className="w3-toggle-row">
          <input
            type="checkbox"
            checked={gitBranchPerInitiative}
            onChange={(e) => toggleGitBranchPerInitiative(e.target.checked)}
          />
          Branch per initiative
        </label>
        <p className="muted">
          Checks out <code>idea/&lt;name&gt;</code> the first time this initiative applies, and
          reuses that branch after. Refused if the workspace has unrelated uncommitted changes —
          nothing of yours is swept into an initiative's branch by accident.
        </p>

        <p className="muted w3-git-note">
          Agent commits always run with git hooks disabled, whichever of these are on.
        </p>
      </section>

      {/* Review rigor (B1/D1): a one-line explanation per level, so the
          choice is legible rather than a bare enum. */}
      <section className="card">
        <h2>Review rigor</h2>
        <label className="field-label" htmlFor="review-rigor-select">
          Rigor
        </label>
        <select
          id="review-rigor-select"
          className="text-input"
          value={reviewRigor}
          onChange={(e) => saveReviewRigor(e.target.value)}
        >
          {REVIEW_RIGOR_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        <ul className="w3-rigor-list">
          {REVIEW_RIGOR_OPTIONS.map((o) => (
            <li key={o.value} className={o.value === reviewRigor ? "w3-rigor-active" : ""}>
              <strong>{o.label}.</strong> {o.explanation}
            </li>
          ))}
        </ul>
      </section>

      {/* Secret exemptions (review changes #1): every "not a secret"
          disposition, visible and revocable, rather than an invisible
          standing exception. */}
      <section className="card">
        <h2>Secret exemptions</h2>
        <p className="muted">
          When a real credential value is marked "not a secret", its fingerprint (never the value
          itself) is allowed from then on for this project, at the path it was found in. Revoking
          an exemption makes the sweep deny that value again.
        </p>
        {allowlist.length === 0 ? (
          <p className="muted">No exemptions. Every secret-sweep finding still blocks.</p>
        ) : (
          <ul className="list w3-allowlist">
            {allowlist.map((e) => (
              <li key={e.id} className="w3-allowlist-row">
                <div>
                  <strong>{e.kind}</strong>{" "}
                  <span className="muted">({allowlistFingerprintPrefix(e.fingerprint)}…)</span>
                  <div className="muted">{e.path}</div>
                  <div className="muted">
                    {e.actor} · {new Date(e.created_at).toLocaleString()}
                  </div>
                </div>
                <button className="ghost" onClick={() => revokeAllowlistEntry(e.id)}>
                  Revoke
                </button>
              </li>
            ))}
          </ul>
        )}
        {allowlistError && <p className="error">{allowlistError}</p>}
      </section>

      <IntegrationsPanel projectId={projectId} />
      <NotificationsPanel projectId={projectId} />
      <RemoteAccessPanel />

      {saved && <p className="ok-text">{saved}</p>}
      {warning && <p className="warn-text">{warning}</p>}
      {error && <p className="error">{error}</p>}
    </div>
  );
}
