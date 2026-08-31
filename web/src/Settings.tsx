import { useCallback, useEffect, useState } from "react";
import { get, put } from "./api";

interface Provider {
  name: string;
  label: string;
  type: string;
  base_url: string;
  key_env: string;
  ready: boolean;
  configured: boolean;
  is_default: boolean;
  models: string[];
}

interface ProvidersView {
  default_model: string;
  providers: Provider[];
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

  const load = useCallback(async () => {
    try {
      const v = await get<ProvidersView>(`/projects/${projectId}/providers`);
      setView(v);
      setDefaultModel(v.default_model || "");
      const p = v.default_model?.includes("/")
        ? v.default_model.split("/")[0]
        : "openai";
      setSelectedProvider(p);
    } catch (e) {
      setError(String(e));
    }
  }, [projectId]);

  useEffect(() => {
    load();
  }, [load]);

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
    <main className="home">
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
              {p.ready ? "" : p.configured ? " (key missing)" : " (not configured)"}
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
                <span className={`dot ${p.ready ? "ready" : ""}`} />
                <div className="muted">
                  {p.type} · {p.base_url || "default endpoint"}
                  {p.key_env ? ` · key: ${p.key_env}` : " · no key (local)"}
                  {!p.ready && p.configured && " · key not set"}
                </div>
              </div>
              <span className="muted">{p.ready ? "ready" : p.configured ? "missing key" : "not configured"}</span>
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

      {saved && <p className="ok-text">{saved}</p>}
      {error && <p className="error">{error}</p>}
    </main>
  );
}
