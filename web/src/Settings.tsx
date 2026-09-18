import { useCallback, useEffect, useState } from "react";
import { get, put } from "./api";
import {
  providerDetail,
  providerOptionSuffix,
  providerReadinessWarning,
  providerState,
  providerStatusLabel,
  modelsLoadedMessage,
} from "./providerStatus";
import {
  executorImagePlaceholder,
  executorStatusLines,
  type ExecutorStatusInput,
} from "./executorStatus";
import { chipsToShow, orderModels } from "./modelCatalog";

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
  demo?: boolean;
  providers: Provider[];
}

interface ExecutorInfo {
  name: string;
  trust: string;
  sandbox_mode: string;
  network: string;
  capabilities: string[];
  selected: boolean;
  image?: string;
  model?: string;
  default_image?: string;
  // Added alongside this feature: image/key readiness and, when not ready,
  // the reason (C3). Optional so a daemon that predates it still renders.
  status?: ExecutorStatusInput;
}

const EXECUTOR_KINDS = ["internal", "container", "omp", "hermes"];

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
  const [selectedProvider, setSelectedProvider] = useState("openai");
  const [liveModels, setLiveModels] = useState<string[]>([]);
  const [loadingModels, setLoadingModels] = useState(false);
  const [modelQuery, setModelQuery] = useState("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  // A readiness nudge shown alongside a successful save, not instead of it
  // (F4): saving a model/executor pointed at an unready provider still
  // succeeds — the daemon just can't call it yet.
  const [warning, setWarning] = useState("");

  // add-provider form state
  const [preset, setPreset] = useState("openai");
  const [baseUrl, setBaseUrl] = useState("");
  const [keyEnv, setKeyEnv] = useState("");

  // executor form state
  const [executors, setExecutors] = useState<ExecutorInfo[]>([]);
  const [executorKind, setExecutorKind] = useState("internal");
  const [executorImage, setExecutorImage] = useState("");
  const [executorModel, setExecutorModel] = useState("");
  const [initialKind, setInitialKind] = useState("internal");
  const [initialImage, setInitialImage] = useState("");
  const [initialModel, setInitialModel] = useState("");
  // The reason a 400 gave for the model field specifically (C3), shown next
  // to that field instead of only in the page-level error banner.
  const [executorModelError, setExecutorModelError] = useState("");

  // git commit on apply toggle
  const [gitCommit, setGitCommit] = useState(false);

  // loadLiveModels fetches one provider's live listing, orders it
  // curated-first (I2), and reports either the count or — for a provider
  // that cannot actually be called — why not, instead of "Loaded 0 models"
  // (I3/F4). `viewOverride` lets `load()` below pass the ProvidersView it
  // just fetched, since the `view` state it would otherwise close over has
  // not been committed yet on that first render.
  const loadLiveModels = useCallback(
    async (provider: string, viewOverride?: ProvidersView | null) => {
      setLoadingModels(true);
      setError("");
      try {
        const v = await get<{ provider: string; models: string[] }>(
          `/projects/${projectId}/models?provider=${provider}`,
        );
        const ordered = orderModels(provider, v.models ?? []);
        setLiveModels(ordered.map((m) => `${provider}/${m}`));
        const entry = (viewOverride ?? view)?.providers.find((pr) => pr.name === provider);
        setSaved(
          entry
            ? modelsLoadedMessage(entry, ordered.length)
            : `Loaded ${ordered.length} models from ${provider}.`,
        );
      } catch (e) {
        setError(String(e));
      } finally {
        setLoadingModels(false);
      }
    },
    [projectId, view],
  );

  const load = useCallback(async () => {
    try {
      const v = await get<ProvidersView>(`/projects/${projectId}/providers`);
      setView(v);
      setDemo(v.demo ?? false);
      setDefaultModel(v.default_model || "");
      setGitCommit(v.git_commit ?? false);
      const p = v.default_model?.includes("/")
        ? v.default_model.split("/")[0]
        : "openai";
      setSelectedProvider(p);
      // I1: load the default provider's models on mount instead of leaving
      // the chip grid empty until the user switches provider and back.
      await loadLiveModels(p, v);
    } catch (e) {
      setError(String(e));
    }
  }, [projectId, loadLiveModels]);

  const loadExecutors = useCallback(async () => {
    try {
      const list = await get<ExecutorInfo[]>(`/projects/${projectId}/executors`);
      setExecutors(list);
      const selected = list.find((e) => e.selected);
      if (selected && EXECUTOR_KINDS.includes(selected.name)) {
        setExecutorKind(selected.name);
        setInitialKind(selected.name);
        // C3: show what is actually stored, never a default it never saved.
        // This used to fall back to `selected.default_image`, which meant a
        // reload showed an image nobody had chosen — that fallback moved to
        // the field's placeholder (executorImagePlaceholder) instead.
        setExecutorImage(selected.image || "");
        setInitialImage(selected.image || "");
        setExecutorModel(selected.model || "");
        setInitialModel(selected.model || "");
      }
    } catch {
      // executors endpoint may not exist yet; leave defaults in place.
    }
  }, [projectId]);

  useEffect(() => {
    load();
  }, [load]);

  useEffect(() => {
    loadExecutors();
  }, [loadExecutors]);

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
      setError(String(e));
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
      setError(String(e));
    }
  };

  const saveExecutor = async () => {
    setError("");
    setSaved("");
    setWarning("");
    setExecutorModelError("");
    const body: { kind?: string; image?: string; model?: string } = {};
    if (executorKind !== initialKind) body.kind = executorKind;
    if (executorImage !== initialImage) body.image = executorImage;
    if (executorModel !== initialModel) body.model = executorModel;
    if (Object.keys(body).length === 0) {
      setSaved("Executor is unchanged.");
      return;
    }
    try {
      await put(`/projects/${projectId}/executors`, body);
      setSaved("Executor saved.");
      setInitialKind(executorKind);
      setInitialImage(executorImage);
      setInitialModel(executorModel);
      // F4: a saved executor model can still name a provider that is not
      // ready — same nudge as the model picker, next to a different action.
      if (executorModel.includes("/")) {
        const entry = view?.providers.find((p) => p.name === executorModel.split("/")[0]);
        if (entry) setWarning(providerReadinessWarning(entry) ?? "");
      }
      await loadExecutors();
    } catch (e) {
      // C3: a 400 here is executor.ResolveAgentProvider refusing the model
      // (unknown provider, bad name/type) — shown next to the Model field,
      // since that is the one field a 400 can name a reason for.
      setExecutorModelError(errorMessage(e));
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
      setError(String(e));
      setGitCommit(!checked);
    }
  };

  const models = Array.from(new Set([...allModels(), ...liveModels]));

  const selectProvider = (provider: string) => {
    setSelectedProvider(provider);
    setLiveModels([]);
    loadLiveModels(provider);
  };

  // I2/I3: liveModels is already curated-first and chat-only (orderModels,
  // in loadLiveModels above); this just bounds how many render at once and
  // lets a typed query reach past that bound (I "Bound OpenRouter's 446
  // chips").
  const visibleModels = chipsToShow(liveModels, modelQuery);

  // The full executors list (one entry per kind, C3) so the Image
  // placeholder and status panel track whichever kind is selected in the
  // dropdown right now, not only the one already saved.
  const executorEntry = executors.find((e) => e.name === executorKind);

  return (
    <div className="settings-page">
      {demo && (
        <div className="banner">
          Demo mode — synthetic output. Configure a model provider to run real agents.
        </div>
      )}
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
              <span className="muted">{providerStatusLabel(p)}</span>
            </li>
          ))}
        </ul>
      </section>

      <section className="card">
        <h2>Add provider</h2>
        <label className="field-label">Provider</label>
        <select
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
        <label className="field-label">Base URL</label>
        <input
          className="text-input"
          value={baseUrl}
          placeholder="endpoint URL"
          onChange={(e) => setBaseUrl(e.target.value)}
        />
        <label className="field-label">API key environment variable</label>
        <input
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

      <section className="card">
        <h2>Executor</h2>

        <label className="field-label" htmlFor="executor-kind">
          Kind
        </label>
        <select
          id="executor-kind"
          className="text-input"
          value={executorKind}
          onChange={(e) => setExecutorKind(e.target.value)}
        >
          {EXECUTOR_KINDS.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </select>

        <label className="field-label" htmlFor="executor-image">
          Image
        </label>
        <input
          id="executor-image"
          className="text-input"
          value={executorImage}
          placeholder={executorImagePlaceholder(executorEntry?.default_image)}
          onChange={(e) => setExecutorImage(e.target.value)}
        />

        <label className="field-label" htmlFor="executor-model">
          Model
        </label>
        <input
          id="executor-model"
          className="text-input"
          value={executorModel}
          placeholder="provider/model — leave empty for default"
          onChange={(e) => setExecutorModel(e.target.value)}
        />
        {/* C3: a 400 from PUT .../executors names the reason a model's
            provider can't be used — shown next to the field it's about. */}
        {executorModelError && <p className="error">{executorModelError}</p>}

        {/* C3: image/key readiness for whichever kind is selected above, so
            switching kinds previews what would happen before Save is even
            clicked. */}
        {executorEntry?.status && (
          <div className="executor-status">
            {(() => {
              const lines = executorStatusLines(executorEntry.status!);
              return (
                <>
                  <div className={`executor-status-line ${lines.imageLine.ok ? "ok" : "warn"}`}>
                    <span className="executor-status-dot" />
                    {lines.imageLine.text}
                  </div>
                  {lines.keyLine && (
                    <div className={`executor-status-line ${lines.keyLine.ok ? "ok" : "warn"}`}>
                      <span className="executor-status-dot" />
                      {lines.keyLine.text}
                    </div>
                  )}
                  {lines.problemLine && (
                    <div className="executor-status-problem">{lines.problemLine}</div>
                  )}
                </>
              );
            })()}
          </div>
        )}

        {(executorKind === "omp" || executorKind === "hermes") && (
          <p className="warn-text">
            Runs with network egress + auto-approve and receives its provider
            API key. Only use an image you trust.
          </p>
        )}
        {executorKind === "internal" && (
          <p className="muted" style={{ marginTop: 12 }}>
            placeholder executor — writes no real code. Select hermes/omp for
            real code.
          </p>
        )}

        <div className="row">
          <button onClick={saveExecutor}>Save executor</button>
        </div>
      </section>

      <section className="card">
        <h2>Apply</h2>
        <label
          style={{
            display: "flex",
            alignItems: "center",
            gap: 8,
            fontSize: "0.95rem",
          }}
        >
          <input
            type="checkbox"
            checked={gitCommit}
            onChange={(e) => toggleGitCommit(e.target.checked)}
          />
          git commit on apply
        </label>
        <p className="muted" style={{ marginTop: 8 }}>
          Automatically commit changes when an initiative is applied.
        </p>
      </section>

      {saved && <p className="ok-text">{saved}</p>}
      {warning && <p className="warn-text">{warning}</p>}
      {error && <p className="error">{error}</p>}
    </div>
  );
}
