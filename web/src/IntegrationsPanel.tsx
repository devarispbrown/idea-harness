import { useCallback, useEffect, useState } from "react";
import { get, post } from "./api";
import {
  integrationDetails,
  integrationHeadline,
  integrationLabel,
  integrationState,
  type IntegrationsView,
} from "./integrationStatus";

interface TestResult {
  ok: boolean;
  error?: string;
}

// IntegrationsPanel is Settings → Integrations: read-only health for Slack
// and Linear plus a "Send test" button per configured integration.
// Configuration itself stays in harness.yaml and .env (docs/integrations.md):
// tokens must never pass through the browser.
export default function IntegrationsPanel({ projectId }: { projectId: string }) {
  const [view, setView] = useState<IntegrationsView | null>(null);
  const [error, setError] = useState("");
  const [testing, setTesting] = useState("");
  const [results, setResults] = useState<Record<string, TestResult>>({});

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
    <section className="card">
      <h2>Integrations</h2>
      <p className="muted">
        Slack notifications and approvals, Linear issue sync. Configured in harness.yaml under{" "}
        <code>integrations</code>, with tokens in the environment or <code>.env</code>; see
        docs/integrations.md.
      </p>
      {error && <p className="error">{error}</p>}
      {view && (
        <ul className="list">
          {view.integrations.map((s) => {
            const state = integrationState(s);
            const result = results[s.name];
            return (
              <li key={s.name}>
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
                {s.configured && (
                  <button className="ghost" disabled={testing !== ""} onClick={() => runTest(s.name)}>
                    {testing === s.name ? "Testing…" : s.name === "slack" ? "Send test message" : "Test connection"}
                  </button>
                )}
                {result && (
                  <p className={result.ok ? "ok-text" : "error"}>{result.ok ? "Test passed." : result.error}</p>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
