import { useCallback, useEffect, useRef, useState } from "react";
import { get, post, put } from "../api";
import {
  executorImagePlaceholder,
  executorResolutionLine,
  executorStatusLines,
  hasExecutorStatusLines,
  isModelFieldError,
  requestedExecutorKind,
  type ExecutorResolution,
  type ExecutorStatusInput,
} from "../executorStatus";
import { formatSmokeElapsed, lastSmokeMessage, smokeButtonState } from "../smokeTest";
import {
  EXECUTOR_KINDS,
  TRUST,
  emptyMCPServer,
  executorFormFrom,
  executorPutBody,
  usesImage,
  usesModel,
  validateExecutor,
  type ExecutorConfigView,
  type ExecutorForm,
  type FieldErrors,
  type MCPServerForm,
} from "./executorForm";

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
  status?: ExecutorStatusInput;
  resolution?: ExecutorResolution;
}

function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

// ExecutorSettings is Settings → Executor profile: the whole `executor`
// block of harness.yaml -- kind, image, model, and each kind's own fields
// (ACP command, key variable and turn timeout; OpenHands server, agent, key
// withholding and UI link; MCP servers for acp) -- beside each kind's trust
// trade-off, the readiness panel, the executor/model compatibility advisory,
// and the smoke test that proves the SAVED profile works.
//
// It reads GET .../executors/config (the block as written) and GET
// .../executors (readiness per kind), and writes PUT .../executors with only
// the fields that changed.
export default function ExecutorSettings({ projectId }: { projectId: string }) {
  const [executors, setExecutors] = useState<ExecutorInfo[]>([]);
  const [view, setView] = useState<ExecutorConfigView | null>(null);
  const [initial, setInitial] = useState<ExecutorForm>(() => executorFormFrom(null));
  const [form, setForm] = useState<ExecutorForm>(() => executorFormFrom(null));
  const [errors, setErrors] = useState<FieldErrors>({});
  const [compat, setCompat] = useState<ExecutorConfigView["compat"]>(null);
  const [error, setError] = useState("");
  const [modelError, setModelError] = useState("");
  const [saved, setSaved] = useState("");
  const [warnings, setWarnings] = useState<string[]>([]);

  const [smokeOpId, setSmokeOpId] = useState<string | null>(null);
  const [smokeStartedAt, setSmokeStartedAt] = useState<number | null>(null);
  const [smokeNow, setSmokeNow] = useState(() => Date.now());
  const [smokeResult, setSmokeResult] = useState<"succeeded" | "failed" | "cancelled" | null>(null);
  const [smokeError, setSmokeError] = useState("");
  const [smokeStartError, setSmokeStartError] = useState("");

  const base = `/projects/${encodeURIComponent(projectId)}/executors`;

  const loadExecutors = useCallback(async () => {
    try {
      setExecutors(await get<ExecutorInfo[]>(base));
    } catch {
      // executors endpoint may not exist yet; the form still works.
    }
  }, [base]);

  const loadConfig = useCallback(async () => {
    try {
      const v = await get<ExecutorConfigView>(`${base}/config`);
      setView(v);
      setCompat(v.compat);
      const f = executorFormFrom(v.config);
      setInitial(f);
      setForm(f);
    } catch (e) {
      // A daemon that predates GET /executors/config: fall back to the list's
      // kind/image/model so the basic fields still edit.
      try {
        const list = await get<ExecutorInfo[]>(base);
        const sel = list.find((x) => x.selected);
        const f = executorFormFrom({ kind: sel?.name ?? requestedExecutorKind(list), image: sel?.image, model: sel?.model });
        setInitial(f);
        setForm(f);
      } catch {
        setError(errorMessage(e));
      }
    }
  }, [base]);

  useEffect(() => {
    loadConfig();
    loadExecutors();
  }, [loadConfig, loadExecutors]);

  // The compatibility advisory follows the form, not just the saved profile:
  // picking hermes with a known-bad model warns before Save. Debounced.
  useEffect(() => {
    if (!view) return;
    const t = setTimeout(async () => {
      try {
        const q = new URLSearchParams({ kind: form.kind, model: form.model });
        const v = await get<ExecutorConfigView>(`${base}/config?${q.toString()}`);
        setCompat(v.compat);
      } catch {
        /* keep the last advisory */
      }
    }, 400);
    return () => clearTimeout(t);
  }, [form.kind, form.model, base, view]);

  useEffect(() => {
    if (!smokeOpId) return;
    let cancelled = false;
    const poll = async () => {
      let op: { status: string; error?: string };
      try {
        op = await get<{ status: string; error?: string }>(`/operations/${smokeOpId}`);
      } catch {
        return;
      }
      if (cancelled || op.status === "running" || op.status === "pending") return;
      setSmokeResult(op.status as "succeeded" | "failed" | "cancelled");
      setSmokeError(op.error ?? "");
      setSmokeOpId(null);
      loadExecutors();
    };
    poll();
    const id = setInterval(poll, 1500);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, [smokeOpId, loadExecutors]);

  useEffect(() => {
    if (!smokeOpId) return;
    const id = setInterval(() => setSmokeNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [smokeOpId]);

  const set = <K extends keyof ExecutorForm>(k: K, v: ExecutorForm[K]) => setForm((f) => ({ ...f, [k]: v }));
  const setMCP = (i: number, m: Partial<MCPServerForm>) =>
    setForm((f) => ({ ...f, mcp: f.mcp.map((x, j) => (j === i ? { ...x, ...m } : x)) }));

  const save = async () => {
    setError("");
    setSaved("");
    setModelError("");
    setWarnings([]);
    const e = validateExecutor(form);
    setErrors(e);
    if (Object.keys(e).length > 0) return;
    const body = executorPutBody(initial, form);
    if (Object.keys(body).length === 0) {
      setSaved("Executor is unchanged.");
      return;
    }
    try {
      const res = await put<{ status: string; warnings?: string[] }>(base, body);
      setSaved("Executor saved.");
      setWarnings(res.warnings ?? []);
      await loadConfig();
      await loadExecutors();
    } catch (err) {
      const msg = errorMessage(err);
      if (isModelFieldError(msg)) setModelError(msg);
      else setError(msg);
    }
  };

  const smokeStarting = useRef(false);
  const runSmoke = async () => {
    if (smokeStarting.current || smokeOpId) return;
    smokeStarting.current = true;
    setSmokeStartError("");
    setSmokeError("");
    setSmokeResult(null);
    setSmokeStartedAt(Date.now());
    try {
      const res = await post<{ operation_id: string }>(`${base}/smoke`, {});
      setSmokeOpId(res.operation_id);
    } catch (e) {
      setSmokeStartedAt(null);
      setSmokeStartError(errorMessage(e));
      loadExecutors();
    } finally {
      smokeStarting.current = false;
    }
  };

  const resolvedRow = executors.find((e) => e.resolution);
  const entry = form.kind === "auto" ? resolvedRow : executors.find((e) => e.name === form.kind);
  const savedRow = executors.find((e) => e.selected) ?? (initial.kind === "auto" ? resolvedRow : undefined);
  const dirty = Object.keys(executorPutBody(initial, form)).length > 0;
  const otherSmokeRunning = !smokeOpId && ["running", "pending"].includes(savedRow?.status?.last_smoke?.status ?? "");
  useEffect(() => {
    if (!otherSmokeRunning) return;
    const id = setInterval(() => loadExecutors(), 3000);
    return () => clearInterval(id);
  }, [otherSmokeRunning, loadExecutors]);
  const smokeState = smokeButtonState(savedRow?.name ?? "", savedRow?.status);
  const lastSmokeLine = lastSmokeMessage(savedRow?.status?.last_smoke, smokeNow);
  const trust = TRUST[form.kind];
  const fieldError = (k: string) =>
    errors[k] ? (
      <p className="error" id={`exec-${k}-error`}>
        {errors[k]}
      </p>
    ) : null;
  const aria = (k: string) => (errors[k] ? { "aria-invalid": true, "aria-describedby": `exec-${k}-error` } : {});

  return (
    <section className="card" aria-labelledby="executor-title">
      <h2 id="executor-title">Executor</h2>

      <label className="field-label" htmlFor="executor-kind">
        Kind
      </label>
      <select id="executor-kind" className="text-input" value={form.kind} onChange={(e) => set("kind", e.target.value)}>
        {EXECUTOR_KINDS.map((k) => (
          <option key={k} value={k}>
            {k}
          </option>
        ))}
      </select>
      {trust && (
        <p className={`exec-trust exec-trust-${trust.level}`} data-testid="executor-trust">
          <strong>{trust.level === "none" ? "Nothing leaves this machine." : trust.level === "remote" ? "Runs on another machine." : "Runs locally, with network and your key."}</strong>{" "}
          {trust.text}
        </p>
      )}

      {usesImage(form.kind) && (
        <>
          <label className="field-label" htmlFor="executor-image">
            Image
          </label>
          <input
            id="executor-image"
            className="text-input"
            value={form.image}
            placeholder={executorImagePlaceholder(entry?.default_image)}
            onChange={(e) => set("image", e.target.value)}
            {...aria("image")}
          />
          {fieldError("image")}
        </>
      )}

      {usesModel(form.kind) && (
        <>
          <label className="field-label" htmlFor="executor-model">
            Model
          </label>
          <input
            id="executor-model"
            className="text-input"
            value={form.model}
            placeholder="provider/model — leave empty for default"
            onChange={(e) => set("model", e.target.value)}
          />
          {modelError && <p className="error">{modelError}</p>}
        </>
      )}
      {compat && (
        <p className={compat.blocked ? "error" : "warn-text"} role="status">
          {compat.blocked ? "Known not to work: " : "Untested: "}
          {compat.message}
        </p>
      )}

      {form.kind === "acp" && (
        <fieldset className="exec-group">
          <legend>ACP agent</legend>
          <label className="field-label" htmlFor="executor-acp-command">
            Agent command inside the image
          </label>
          <input
            id="executor-acp-command"
            className="text-input"
            placeholder='claude-code-acp   or   ["gemini","--experimental-acp"]'
            value={form.acpCommand}
            onChange={(e) => set("acpCommand", e.target.value)}
            {...aria("acpCommand")}
          />
          {fieldError("acpCommand")}
          <label className="field-label" htmlFor="executor-acp-key-env">
            Provider key's name inside the container (optional)
          </label>
          <input
            id="executor-acp-key-env"
            className="text-input"
            placeholder="the provider's own variable, e.g. GEMINI_API_KEY"
            value={form.acpKeyEnv}
            onChange={(e) => set("acpKeyEnv", e.target.value)}
            {...aria("acpKeyEnv")}
          />
          {fieldError("acpKeyEnv")}
          <label className="field-label" htmlFor="executor-acp-timeout">
            Turn timeout (seconds)
          </label>
          <input
            id="executor-acp-timeout"
            className="text-input"
            inputMode="numeric"
            placeholder="840"
            value={form.acpTimeoutSeconds}
            onChange={(e) => set("acpTimeoutSeconds", e.target.value)}
            {...aria("acpTimeoutSeconds")}
          />
          {fieldError("acpTimeoutSeconds")}
        </fieldset>
      )}

      {form.kind === "openhands" && (
        <fieldset className="exec-group">
          <legend>OpenHands Agent Server</legend>
          <label className="field-label" htmlFor="executor-oh-url">
            Server URL
          </label>
          <input
            id="executor-oh-url"
            className="text-input"
            placeholder="https://agents.example.com"
            value={form.ohURL}
            onChange={(e) => set("ohURL", e.target.value)}
            {...aria("ohURL")}
          />
          {fieldError("ohURL")}
          <label className="field-label" htmlFor="executor-oh-agent">
            Agent
          </label>
          <select id="executor-oh-agent" className="text-input" value={form.ohAgent} onChange={(e) => set("ohAgent", e.target.value)}>
            <option value="">openhands (the server's own agent)</option>
            <option value="acp">acp (an ACP agent the server's image carries)</option>
          </select>
          {form.ohAgent === "acp" && (
            <>
              <label className="field-label" htmlFor="executor-oh-acp-command">
                ACP agent command on the server
              </label>
              <input
                id="executor-oh-acp-command"
                className="text-input"
                placeholder="claude-agent-acp"
                value={form.ohACPCommand}
                onChange={(e) => set("ohACPCommand", e.target.value)}
                {...aria("ohACPCommand")}
              />
              {fieldError("ohACPCommand")}
            </>
          )}
          <label className="field-label" htmlFor="executor-oh-key-env">
            Session key variable
          </label>
          <input
            id="executor-oh-key-env"
            className="text-input"
            placeholder="OPENHANDS_API_KEY"
            value={form.ohAPIKeyEnv}
            onChange={(e) => set("ohAPIKeyEnv", e.target.value)}
            {...aria("ohAPIKeyEnv")}
          />
          {fieldError("ohAPIKeyEnv")}
          {view &&
            Object.entries(view.env)
              .filter(([env]) => env === (form.ohAPIKeyEnv || "OPENHANDS_API_KEY"))
              .map(([env, isSet]) => (
                <p key={env} className={isSet ? "muted" : "warn-text"}>
                  <code>{env}</code> {isSet ? "is set." : "is not set (fine if the server was started without a key)."}
                </p>
              ))}
          <label className="w3-toggle-row">
            <input type="checkbox" checked={form.ohWithholdKey} onChange={(e) => set("ohWithholdKey", e.target.checked)} />
            Keep the coding model's provider key on this machine (the server supplies its own)
          </label>
          <label className="field-label" htmlFor="executor-oh-ui-url">
            OpenHands web UI (optional, for "Open in OpenHands" links)
          </label>
          <input
            id="executor-oh-ui-url"
            className="text-input"
            placeholder="https://openhands.example.com"
            value={form.ohUIURL}
            onChange={(e) => set("ohUIURL", e.target.value)}
            {...aria("ohUIURL")}
          />
          {fieldError("ohUIURL")}
        </fieldset>
      )}

      {(form.kind === "acp" || form.mcp.length > 0) && (
        <fieldset className="exec-group" data-testid="mcp-servers">
          <legend>MCP servers {form.kind !== "acp" && <span className="warn-text">(only used by the acp kind)</span>}</legend>
          <p className="muted">
            A stdio server runs inside the agent's container, with the agent's egress and auto-approved permissions. An
            http/sse server is reached by the agent, which sends it whatever its headers carry. Secrets are never inline:
            give the NAME of a variable for anything that is a credential.
          </p>
          {form.mcp.map((m, i) => (
            <div key={i} className="mcp-server">
              <div className="row">
                <label className="field-label" htmlFor={`mcp-${i}-name`}>
                  Name
                </label>
                <input id={`mcp-${i}-name`} className="text-input" value={m.name} onChange={(e) => setMCP(i, { name: e.target.value })} {...aria(`mcp.${i}.name`)} />
                <label className="field-label" htmlFor={`mcp-${i}-type`}>
                  Transport
                </label>
                <select id={`mcp-${i}-type`} className="text-input" value={m.type} onChange={(e) => setMCP(i, { type: e.target.value as MCPServerForm["type"] })}>
                  <option value="stdio">stdio</option>
                  <option value="http">http</option>
                  <option value="sse">sse</option>
                </select>
                <button className="ghost" onClick={() => setForm((f) => ({ ...f, mcp: f.mcp.filter((_, j) => j !== i) }))}>
                  Remove
                </button>
              </div>
              {fieldError(`mcp.${i}.name`)}
              {m.type === "stdio" ? (
                <>
                  <label className="field-label" htmlFor={`mcp-${i}-command`}>
                    Command
                  </label>
                  <input id={`mcp-${i}-command`} className="text-input" value={m.command} onChange={(e) => setMCP(i, { command: e.target.value })} {...aria(`mcp.${i}.command`)} />
                  {fieldError(`mcp.${i}.command`)}
                  <label className="field-label" htmlFor={`mcp-${i}-args`}>
                    Arguments
                  </label>
                  <input id={`mcp-${i}-args`} className="text-input" value={m.args} onChange={(e) => setMCP(i, { args: e.target.value })} {...aria(`mcp.${i}.args`)} />
                  {fieldError(`mcp.${i}.args`)}
                </>
              ) : (
                <>
                  <label className="field-label" htmlFor={`mcp-${i}-url`}>
                    URL
                  </label>
                  <input id={`mcp-${i}-url`} className="text-input" value={m.url} onChange={(e) => setMCP(i, { url: e.target.value })} {...aria(`mcp.${i}.url`)} />
                  {fieldError(`mcp.${i}.url`)}
                </>
              )}
              <div className="mcp-pairs">
                <span className="field-label">{m.type === "stdio" ? "Environment" : "Headers"}</span>
                {m.pairs.map((p, j) => (
                  <div key={j}>
                    <div className="row">
                      <input
                        aria-label={`${m.type === "stdio" ? "Variable" : "Header"} name`}
                        className="text-input"
                        value={p.name}
                        onChange={(e) => setMCP(i, { pairs: m.pairs.map((x, k) => (k === j ? { ...x, name: e.target.value } : x)) })}
                      />
                      <select
                        aria-label="Value source"
                        className="text-input"
                        value={p.mode}
                        onChange={(e) =>
                          setMCP(i, { pairs: m.pairs.map((x, k) => (k === j ? { ...x, mode: e.target.value as "value" | "value_env" } : x)) })
                        }
                      >
                        <option value="value_env">from variable</option>
                        <option value="value">literal</option>
                      </select>
                      <input
                        aria-label={p.mode === "value_env" ? "Variable name" : "Literal value"}
                        className="text-input"
                        value={p.text}
                        placeholder={p.mode === "value_env" ? "VARIABLE_NAME" : "a non-secret value"}
                        onChange={(e) => setMCP(i, { pairs: m.pairs.map((x, k) => (k === j ? { ...x, text: e.target.value } : x)) })}
                        {...aria(`mcp.${i}.pair.${j}`)}
                      />
                      {p.mode === "value_env" && view && p.text && view.env[p.text] !== undefined && (
                        <span className={view.env[p.text] ? "muted" : "warn-text"}>{view.env[p.text] ? "set" : "not set"}</span>
                      )}
                      <button className="ghost" onClick={() => setMCP(i, { pairs: m.pairs.filter((_, k) => k !== j) })}>
                        Remove
                      </button>
                    </div>
                    {fieldError(`mcp.${i}.pair.${j}`)}
                  </div>
                ))}
                <button className="ghost" onClick={() => setMCP(i, { pairs: [...m.pairs, { name: "", mode: "value_env", text: "" }] })}>
                  Add {m.type === "stdio" ? "variable" : "header"}
                </button>
              </div>
            </div>
          ))}
          <button className="ghost" onClick={() => setForm((f) => ({ ...f, mcp: [...f.mcp, emptyMCPServer()] }))}>
            Add MCP server
          </button>
        </fieldset>
      )}

      {form.kind === "auto" && initial.kind === "auto" && resolvedRow?.resolution && (
        <div className="executor-resolution" role="status">
          <p>{executorResolutionLine(resolvedRow.resolution)}</p>
          {resolvedRow.resolution.warning && <p className="warn-text">{resolvedRow.resolution.warning}</p>}
        </div>
      )}
      {entry?.status && hasExecutorStatusLines(executorStatusLines(entry.status, entry.default_image ?? "")) && (
        <div className="executor-status">
          {(() => {
            const lines = executorStatusLines(entry.status!, entry.default_image ?? "");
            return (
              <>
                {lines.imageLine && (
                  <div className={`executor-status-line ${lines.imageLine.ok ? "ok" : "warn"}`}>
                    <span className="executor-status-dot" />
                    {lines.imageLine.text}
                  </div>
                )}
                {lines.keyLine && (
                  <div className={`executor-status-line ${lines.keyLine.ok ? "ok" : "warn"}`}>
                    <span className="executor-status-dot" />
                    {lines.keyLine.text}
                  </div>
                )}
                {lines.noteLine && <div className="executor-status-line">{lines.noteLine}</div>}
                {lines.problemLine && <div className="executor-status-problem">{lines.problemLine}</div>}
              </>
            );
          })()}
        </div>
      )}
      {view && view.problems.length > 0 && (
        <ul className="list">
          {view.problems.map((p) => (
            <li key={p.field + p.message} className={p.severity === "error" ? "error" : "warn-text"}>
              {p.message}
            </li>
          ))}
        </ul>
      )}

      <div className="row">
        <button onClick={save}>Save executor</button>
        {dirty && (
          <button className="ghost" onClick={() => { setForm(initial); setErrors({}); }}>
            Discard changes
          </button>
        )}
      </div>
      {saved && <p className="ok-text">{saved}</p>}
      {warnings.map((w) => (
        <p key={w} className="warn-text">
          {w}
        </p>
      ))}
      {error && <p className="error">{error}</p>}

      <div className="smoke-test">
        <div className="row">
          <button className="cs-secondary" disabled={!smokeState.enabled || smokeOpId !== null || dirty} onClick={runSmoke}>
            {smokeOpId !== null
              ? `Running smoke test… ${formatSmokeElapsed(smokeNow - (smokeStartedAt ?? smokeNow))}`
              : "Run smoke test"}
          </button>
        </div>
        <p className="muted">Makes one small real model call using your key — this isn't a dry run.</p>
        {dirty ? (
          <p className="muted">Save the executor first — the smoke test runs the saved setup.</p>
        ) : (
          !smokeState.enabled &&
          smokeState.reason &&
          smokeState.reason !== savedRow?.status?.problem && <p className="muted">{smokeState.reason}</p>
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
        {smokeOpId === null && smokeResult === null && lastSmokeLine && lastSmokeLine !== smokeState.reason && (
          <p className="muted">{lastSmokeLine}</p>
        )}
      </div>
    </section>
  );
}
