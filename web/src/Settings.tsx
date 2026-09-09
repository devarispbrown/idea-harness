import { useCallback, useEffect, useState } from "react";
import { get, put } from "./api";
import {
  providerDetail,
  providerOptionSuffix,
  providerState,
  providerStatusLabel,
} from "./providerStatus";

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
}

const EXECUTOR_KINDS = ["internal", "container", "omp", "hermes"];

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

  // add-provider form state
  const [preset, setPreset] = useState("openai");
  const [baseUrl, setBaseUrl] = useState("");
  const [keyEnv, setKeyEnv] = useState("");

  // executor form state
  const [executorKind, setExecutorKind] = useState("internal");
  const [executorImage, setExecutorImage] = useState("");
  const [executorModel, setExecutorModel] = useState("");
  const [initialKind, setInitialKind] = useState("internal");
  const [initialImage, setInitialImage] = useState("");
  const [initialModel, setInitialModel] = useState("");

  // git commit on apply toggle
  const [gitCommit, setGitCommit] = useState(false);

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
    } catch (e) {
      setError(String(e));
    }
  }, [projectId]);

  const loadExecutors = useCallback(async () => {
    try {
      const executors = await get<ExecutorInfo[]>(
        `/projects/${projectId}/executors`,
      );
      const selected = executors.find((e) => e.selected);
      if (selected && EXECUTOR_KINDS.includes(selected.name)) {
        setExecutorKind(selected.name);
        setInitialKind(selected.name);
        // Prefill image/model from the persisted config, falling back to the
        // pinned default image (Hermes) so an empty image never silently runs
        // the agent in a toolchain-less alpine image.
        const image = selected.image || selected.default_image || "";
        setExecutorImage(image);
        setInitialImage(image);
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
    try {
      await put(`/projects/${projectId}/providers`, { default_model: defaultModel });
      setSaved("Saved — will apply to the next initiative.");
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
    } catch (e) {
      setError(String(e));
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

  const loadLiveModels = async (provider: string) => {
    setLoadingModels(true);
    setError("");
    try {
      const v = await get<{ provider: string; models: string[] }>(
        `/projects/${projectId}/models?provider=${provider}`,
      );
      setLiveModels((v.models ?? []).map((m) => `${provider}/${m}`));
      setSaved(`Loaded ${v.models?.length ?? 0} models from ${provider}.`);
    } catch (e) {
      setError(String(e));
    } finally {
      setLoadingModels(false);
    }
  };

  const selectProvider = (provider: string) => {
    setSelectedProvider(provider);
    setLiveModels([]);
    loadLiveModels(provider);
  };

  const visibleModels = liveModels.filter((m) =>
    m.toLowerCase().includes(modelQuery.toLowerCase()),
  );

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
          placeholder="container image (e.g. ghcr.io/org/agent:latest)"
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

        {(executorKind === "omp" || executorKind === "hermes") && (
          <p
            style={{
              marginTop: 12,
              padding: "10px 14px",
              borderRadius: 8,
              background: "#fffbeb",
              border: "1px solid #fde68a",
              color: "#92400e",
              fontSize: "0.9rem",
            }}
          >
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
      {error && <p className="error">{error}</p>}
    </div>
  );
}
