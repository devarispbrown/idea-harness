import { useEffect, useState } from "react";
import { get } from "./api";
import PhaseHeader from "./PhaseHeader";
import { ARTIFACT_TITLES } from "./phases";

interface DiffResp {
  kind: string;
  from_version: number;
  to_version: number;
  diff: string;
}

export default function ArtifactDiffPage({
  projectId,
  from,
  to,
}: {
  projectId: string;
  from: string;
  to: string;
}) {
  const [resp, setResp] = useState<DiffResp | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    get<DiffResp>(`/projects/${projectId}/artifacts/diff?from=${from}&to=${to}`)
      .then((v) => {
        if (!cancelled) setResp(v);
      })
      .catch((e) => {
        if (!cancelled) setError(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [projectId, from, to]);

  return (
    <div className="phase-page">
      <PhaseHeader
        title="What changed"
        purpose={
          resp
            ? `${ARTIFACT_TITLES[resp.kind] ?? resp.kind} · v${resp.from_version} → v${resp.to_version}`
            : "Comparing two artifact versions."
        }
      />
      {error && <p className="error">{error}</p>}
      {!resp && !error && <p className="muted">Loading diff…</p>}
      {resp && (
        <pre className="cs-diff" style={{ maxHeight: "none" }}>
          {resp.diff || "(no change)"}
        </pre>
      )}
    </div>
  );
}
