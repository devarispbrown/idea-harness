import { useState } from "react";
import { get, resource } from "./api";

// RemoteRun mirrors one row of GET …/initiatives/{id}/remote-runs (and of the
// project-wide GET …/remote-runs): an openhands dispatch record
// (internal/daemon/remoteruns.go).
export interface RemoteRun {
  id: string;
  initiative_id?: string;
  task_code?: string;
  attempt: number;
  server_url: string;
  conversation_id?: string;
  status: string;
  remote_status?: string;
  event_count: number;
  last_event?: string;
  error?: string;
  updated_at: string;
}

// RemoteRuns is the run panel's "Remote runs" disclosure, shown when the run's
// executor is openhands: every attempt this initiative dispatched to an Agent
// Server -- which conversation, on which server, where it got to, why it
// failed -- the same rows `idea runs remote` prints. Read when opened, not
// polled: the live run is already the panel above it.
export default function RemoteRuns({ projectId, initiativeId }: { projectId: string; initiativeId: string }) {
  const [runs, setRuns] = useState<RemoteRun[] | null>(null);
  const [error, setError] = useState("");

  const load = async () => {
    setError("");
    try {
      setRuns(await get<RemoteRun[]>(resource(projectId, initiativeId, "remote-runs")));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <details
      className="run-remote-runs"
      onToggle={(e) => {
        if ((e.currentTarget as HTMLDetailsElement).open) void load();
      }}
    >
      <summary>Remote runs</summary>
      {error && <p role="alert">Could not load remote runs: {error}</p>}
      {runs && runs.length === 0 && <p className="muted">No remote runs recorded for this initiative.</p>}
      {runs && runs.length > 0 && <RemoteRunsTable runs={runs} />}
      <p className="muted">
        Also <code>idea runs remote</code>; every initiative's on <a href="#/runs">Remote runs</a>.
      </p>
    </details>
  );
}

// RemoteRunsTable is the rows themselves, shared by the run panel's
// disclosure (one initiative) and RemoteRunsPage (every initiative, which
// passes initiativeTitle to add an Initiative column linking to its Build).
export function RemoteRunsTable({
  runs,
  initiativeTitle,
}: {
  runs: RemoteRun[];
  initiativeTitle?: (id: string) => string;
}) {
  return (
    <table className="run-remote-table automation-table">
      <thead>
        <tr>
          {initiativeTitle && <th scope="col">Initiative</th>}
          <th scope="col">Task</th>
          <th scope="col">Status</th>
          <th scope="col">Attempt</th>
          <th scope="col">Server</th>
          <th scope="col">Conversation</th>
          <th scope="col">Last event</th>
        </tr>
      </thead>
      <tbody>
        {runs.map((r) => (
          <tr key={r.id}>
            {initiativeTitle && (
              <td>{r.initiative_id ? <a href={`#/i/${r.initiative_id}/build`}>{initiativeTitle(r.initiative_id)}</a> : "—"}</td>
            )}
            <td>{r.task_code || "(task removed)"}</td>
            <td>
              {r.status}
              {r.error && <div className="run-error-detail">{r.error}</div>}
            </td>
            <td>{r.attempt}</td>
            <td>{r.server_url}</td>
            <td>
              <code>{r.conversation_id || "(not created yet)"}</code>
            </td>
            <td>
              {r.last_event || <span className="muted">none</span>}
              {r.event_count > 0 && <span className="muted"> ({r.event_count} events)</span>}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
