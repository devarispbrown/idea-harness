import { useCallback, useEffect, useRef, useState } from "react";
import { get, post, put } from "./api";
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
  isModelFieldError,
  type ExecutorStatusInput,
} from "./executorStatus";
import {
  formatSmokeElapsed,
  lastSmokeMessage,
  smokeButtonState,
} from "./smokeTest";
import { chipsToShow, orderModels } from "./modelCatalog";
import { resolveInitialProvider } from "./settingsLoad";

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

  // S3: "Run smoke test". `smokeOpId` is only ever held in memory -- a
  // reload loses track of it on purpose (the daemon has no "operations for
  // this browser tab" concept), and that's fine: `last_smoke` off the
  // executors list is what the button state falls back to after a reload,
  // never a locally-remembered operation id.
  const [smokeOpId, setSmokeOpId] = useState<string | null>(null);
  const [smokeStartedAt, setSmokeStartedAt] = useState<number | null>(null);
  const [smokeNow, setSmokeNow] = useState(() => Date.now());
  const [smokeResult, setSmokeResult] = useState<"succeeded" | "failed" | "cancelled" | null>(null);
  const [smokeError, setSmokeError] = useState("");
  // A refusal from the POST itself (400 wrong kind/model, 409 already
  // running) rather than the operation later failing -- shown the same way,
  // but without an operation to poll.
  const [smokeStartError, setSmokeStartError] = useState("");

  // git commit on apply toggle
  const [gitCommit, setGitCommit] = useState(false);

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
      setWarning("");
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
          setSaved("");
          setWarning(modelsLoadedMessage(entry, ordered.length));
        } else {
          setSaved(
            entry
              ? modelsLoadedMessage(entry, ordered.length)
              : `Loaded ${ordered.length} models from ${provider}.`,
          );
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
      // I1: this sets the provider whose models the effect below preloads;
      // Setting the provider starts the model fetch, in its own effect; load
      // itself never depends on the view, which is what looped.
      setSelectedProvider(resolveInitialProvider(v.default_model || ""));
    } catch (e) {
      setError(errorMessage(e));
    }
  }, [projectId]);

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

  // Mount / explicit-refresh only -- see the comment on `load` above.
  useEffect(() => {
    load();
  }, [load]);

  useEffect(() => {
    loadExecutors();
  }, [loadExecutors]);

  // S3: poll the smoke operation until it leaves pending/running. Cleared
  // whenever smokeOpId changes (a new run replaces the old poll) or the page
  // unmounts.
  useEffect(() => {
    if (!smokeOpId) return;
    let cancelled = false;
    const poll = async () => {
      let op: { status: string; error?: string };
      try {
        op = await get<{ status: string; error?: string }>(`/operations/${smokeOpId}`);
      } catch {
        return; // transient error: keep polling
      }
      if (cancelled) return;
      if (op.status === "running" || op.status === "pending") return;
      setSmokeResult(op.status as "succeeded" | "failed" | "cancelled");
      setSmokeError(op.error ?? "");
      setSmokeOpId(null);
      // Refresh so status.last_smoke (S2) reflects this run immediately,
      // rather than waiting for whatever next triggers loadExecutors.
      loadExecutors();
    };
    poll();
    const id = setInterval(poll, 1500);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [smokeOpId, loadExecutors]);

  // Ticks the elapsed-time display while a smoke test is running; idle
  // (no interval) the rest of the time.
  useEffect(() => {
    if (!smokeOpId) return;
    const id = setInterval(() => setSmokeNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [smokeOpId]);

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
      // Low: this used to route EVERY failure here to the Model field. Only
      // a message that actually names the model (C3: a 400 from
      // executor.ResolveAgentProvider refusing it — unknown provider, bad
      // name/type) belongs next to that field; a bad image reference, an
      // unknown kind, or a transient 5xx belongs on the general error line,
      // where it used to never appear at all.
      const msg = errorMessage(e);
      if (isModelFieldError(msg)) {
        setExecutorModelError(msg);
      } else {
        setError(msg);
      }
    }
  };

  // S3: runs `executor.SmokeAgent` for real, through the SAVED executor
  // (the daemon reads its own config, not whatever's pending in this form) --
  // a small real model call using the project's own key. The result arrives
  // via the poll effect above; this only starts it and surfaces a refusal
  // (400 wrong kind/model, 409 already running) that happens before an
  // operation id even exists.
  const runSmoke = async () => {
    setSmokeStartError("");
    setSmokeError("");
    setSmokeResult(null);
    setSmokeStartedAt(Date.now());
    try {
      const res = await post<{ operation_id: string }>(`/projects/${projectId}/executors/smoke`, {});
      setSmokeOpId(res.operation_id);
    } catch (e) {
      setSmokeStartedAt(null);
      setSmokeStartError(errorMessage(e));
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

  // The full executors list (one entry per kind, C3) so the Image
  // placeholder and status panel track whichever kind is selected in the
  // dropdown right now, not only the one already saved.
  const executorEntry = executors.find((e) => e.name === executorKind);
  // S3: the smoke test runs against the SAVED executor, not the dropdown's
  // unsaved preview -- `last_smoke` is only ever populated on this row (the
  // daemon computes it for `selected` only), and the daemon's own
  // POST .../executors/smoke reads pr.cfg.Harness.Executor.Kind, not
  // anything pending in this form.
  const selectedExecutor = executors.find((e) => e.selected);
  // A smoke started before a reload (or in another tab) has no id here. Keep
  // refreshing the executors until its last_smoke leaves "running", so the
  // result appears instead of "A smoke test is running." forever.
  const otherSmokeRunning =
    !smokeOpId && ["running", "pending"].includes(selectedExecutor?.status?.last_smoke?.status ?? "");
  useEffect(() => {
    if (!otherSmokeRunning) return;
    const id = setInterval(() => loadExecutors(), 3000);
    return () => clearInterval(id);
  }, [otherSmokeRunning, loadExecutors]);
  const smokeState = smokeButtonState(selectedExecutor?.name ?? "", selectedExecutor?.status);
  const smokeRunning = smokeOpId !== null;
  const lastSmokeLine = lastSmokeMessage(selectedExecutor?.status?.last_smoke, smokeNow);

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
                  {lines.noteLine && <div className="executor-status-line">{lines.noteLine}</div>}
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

        {/* S3: proves the SAVED executor setup actually works, from the
            daemon's own environment -- `idea executors setup --smoke` proves
            it from the CLI's, which is not always the same one. */}
        <div className="smoke-test">
          <div className="row">
            <button
              className="cs-secondary"
              disabled={!smokeState.enabled || smokeRunning}
              onClick={runSmoke}
            >
              {smokeRunning
                ? `Running smoke test… ${formatSmokeElapsed(smokeNow - (smokeStartedAt ?? smokeNow))}`
                : "Run smoke test"}
            </button>
          </div>
          <p className="muted">
            Makes one small real model call using your key — this isn't a dry run.
          </p>
          {!smokeState.enabled && smokeState.reason && (
            <p className="muted">{smokeState.reason}</p>
          )}
          {smokeStartError && <p className="error">{smokeStartError}</p>}
          {smokeResult === "succeeded" && <p className="ok-text">Smoke test passed.</p>}
          {(smokeResult === "failed" || smokeResult === "cancelled") && (
            <div className="error">
              {smokeResult === "cancelled" ? "Smoke test was cancelled." : "Smoke test failed."}
              {smokeError && (
                <details>
                  <summary>Details</summary>
                  <pre className="cs-finding-evidence">{smokeError}</pre>
                </details>
              )}
            </div>
          )}
          {!smokeRunning && smokeResult === null && lastSmokeLine && (
            <p className="muted">{lastSmokeLine}</p>
          )}
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
