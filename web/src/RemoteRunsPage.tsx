import { useCallback, useEffect, useState } from "react";
import { get } from "./api";
import { RemoteRunsTable, type RemoteRun } from "./RemoteRuns";
import { displayTitle } from "./title";

// remoteRunsPath is the project-wide listing, optionally only the runs not
// yet over (GET …/remote-runs?active=true).
export function remoteRunsPath(projectId: string, activeOnly: boolean): string {
  return `/projects/${encodeURIComponent(projectId)}/remote-runs${activeOnly ? "?active=true" : ""}`;
}

// RemoteRunsPage is #/runs: every OpenHands remote run in the project,
// across initiatives, newest first (the daemon keeps the newest 200) -- the
// rows `idea runs remote --all` prints. Each row names its initiative and
// links to that initiative's Build, where the run panel shows the live run.
// Polled slowly: a remote run's progress streams to the run panel, not here.
export default function RemoteRunsPage({
  projectId,
  initiatives,
}: {
  projectId: string;
  initiatives: { id: string; title: string; short_title?: string }[];
}) {
  const [runs, setRuns] = useState<RemoteRun[] | null>(null);
  const [error, setError] = useState("");
  const [activeOnly, setActiveOnly] = useState(false);

  const load = useCallback(async () => {
    try {
      setRuns(await get<RemoteRun[]>(remoteRunsPath(projectId, activeOnly)));
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [projectId, activeOnly]);

  useEffect(() => {
    void load();
    const id = setInterval(load, 10000);
    return () => clearInterval(id);
  }, [load]);

  const titleOf = (id: string) => {
    const ini = initiatives.find((i) => i.id === id);
    return ini ? displayTitle(ini) : id.slice(0, 8);
  };

  return (
    <div className="phase-page remote-runs-page">
      <header className="automations-head">
        <h1>Remote runs</h1>
        <p className="muted">
          Every task this project dispatched to an OpenHands Agent Server (<code>executor.kind: openhands</code>), across
          initiatives, newest first: which conversation, on which server, where it got to, and why it failed. Also{" "}
          <code>idea runs remote --all</code>.
        </p>
      </header>
      <label className="w3-toggle-row">
        <input type="checkbox" checked={activeOnly} onChange={(e) => setActiveOnly(e.target.checked)} />
        Only runs not yet over
      </label>
      {error && <p role="alert">Could not load remote runs: {error}</p>}
      {runs && runs.length === 0 && (
        <p className="muted">
          {activeOnly
            ? "No remote run is in progress."
            : "No remote runs yet. They are recorded when a task runs on the openhands executor (Settings › Executor)."}
        </p>
      )}
      {runs && runs.length > 0 && <RemoteRunsTable runs={runs} initiativeTitle={titleOf} />}
    </div>
  );
}
