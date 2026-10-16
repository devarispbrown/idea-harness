import { useCallback, useEffect, useState } from "react";
import { get, post } from "./api";
import {
  integrationDetails,
  integrationHeadline,
  integrationLabel,
  integrationState,
  type IntegrationsView,
} from "./integrationStatus";
import IntegrationEditor from "./settings/IntegrationEditor";
import type { LinearConfig, SlackConfig } from "./settings/integrationForm";

interface TestResult {
  ok: boolean;
  error?: string;
}

// IntegrationsPanel is Settings → Integrations: health for Slack and Linear,
// a "Send test" button per configured integration, and an editor for each
// one's harness.yaml section (settings/IntegrationEditor). Tokens never pass
// through the browser: the editor names the environment variables that hold
// them, and this panel reports whether each is set (docs/integrations.md).
export default function IntegrationsPanel({ projectId }: { projectId: string }) {
  const [view, setView] = useState<IntegrationsView | null>(null);
  const [error, setError] = useState("");
  const [testing, setTesting] = useState("");
  const [results, setResults] = useState<Record<string, TestResult>>({});
  const [editing, setEditing] = useState("");
  const [saved, setSaved] = useState("");
  // Bumped after every save so an open editor remounts on the saved config.
  const [generation, setGeneration] = useState(0);

  const load = useCallback(() => {
    get<IntegrationsView>(`/projects/${encodeURIComponent(projectId)}/integrations`)
      .then((v) => {
        setView(v);
        setError("");
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [projectId]);

  useEffect(() => {
    load();
  }, [load]);

  const runTest = async (name: string) => {
    setTesting(name);
    try {
      const r = await post<TestResult>(
        `/projects/${encodeURIComponent(projectId)}/integrations/${encodeURIComponent(name)}/test`,
        {},
      );
      setResults((prev) => ({ ...prev, [name]: r }));
    } catch (e: unknown) {
      setResults((prev) => ({ ...prev, [name]: { ok: false, error: e instanceof Error ? e.message : String(e) } }));
    } finally {
      setTesting("");
      load();
    }
  };

  return (
    <section className="card" aria-labelledby="integrations-title">
      <h2 id="integrations-title">Integrations</h2>
      <p className="muted">
        Slack notifications and approvals, Linear issue sync. Saved to <code>integrations</code> in harness.yaml; tokens
        stay in the environment or <code>.env</code> and are never shown here. See docs/integrations.md.
      </p>
      {error && <p className="error">{error}</p>}
      {saved && <p className="ok-text">{saved}</p>}
      {view && (
        <ul className="list">
          {view.integrations.map((s) => {
            const state = integrationState(s);
            const result = results[s.name];
            const editable = s.name === "slack" || s.name === "linear";
            return (
              <li key={s.name} data-integration={s.name}>
                <div>
                  <strong>{integrationLabel(s.name)}</strong>{" "}
                  <span className={state === "ok" ? "ok-text" : state === "off" ? "muted" : "warn-text"}>
                    {integrationHeadline(s)}
                  </span>
                </div>
                {integrationDetails(s).map((line) => (
                  <div key={line} className="muted">
                    {line}
                  </div>
                ))}
                {s.problems.map((p) => (
                  <div key={p} className="warn-text">
                    {p}
                  </div>
                ))}
                <div className="row">
                  {s.configured && (
                    <button className="ghost" disabled={testing !== ""} onClick={() => runTest(s.name)}>
                      {testing === s.name ? "Testing…" : s.name === "slack" ? "Send test message" : "Test connection"}
                    </button>
                  )}
                  {editable && (
                    <button
                      className="ghost"
                      aria-expanded={editing === s.name}
                      onClick={() => setEditing(editing === s.name ? "" : s.name)}
                    >
                      {editing === s.name ? "Close" : s.configured ? "Edit settings" : `Set up ${integrationLabel(s.name)}`}
                    </button>
                  )}
                </div>
                {result && (
                  <p className={result.ok ? "ok-text" : "error"}>{result.ok ? "Test passed." : result.error}</p>
                )}
                {editable && editing === s.name && (
                  <IntegrationEditor
                    key={`${s.name}-${generation}`}
                    projectId={projectId}
                    name={s.name as "slack" | "linear"}
                    config={(s.config ?? null) as SlackConfig | LinearConfig | null}
                    tokens={tokensFor(s.tokens, s.token_envs)}
                    eventKinds={view.event_kinds ?? []}
                    defaultEvents={view.default_slack_events ?? []}
                    onSaved={(msg) => {
                      setSaved(msg);
                      setEditing("");
                      setGeneration((g) => g + 1);
                      load();
                    }}
                  />
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

// tokensFor lists every variable the integration reads with whether it is
// set, including the ones the status did not report yet (an integration that
// is not configured has no running hub to ask).
function tokensFor(tokens: Record<string, boolean> | undefined, envs: string[] | undefined): Record<string, boolean> {
  const out: Record<string, boolean> = {};
  for (const env of envs ?? []) out[env] = tokens?.[env] ?? false;
  for (const [env, set] of Object.entries(tokens ?? {})) out[env] = set;
  return out;
}
