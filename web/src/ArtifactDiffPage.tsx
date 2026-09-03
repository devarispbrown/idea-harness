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
        <>
          <p className="sr-only">
            Lines starting with + were added, lines starting with - were removed, and all other
            lines are unchanged context.
          </p>
          {resp.diff ? (
            <div className="diff-lines" role="list">
              {resp.diff.split("\n").map((line, i) => {
                const added = line.startsWith("+ ");
                const removed = line.startsWith("- ");
                const kind = added ? "added" : removed ? "removed" : "context";
                const text = line.slice(2);
                return (
                  <div
                    key={i}
                    role="listitem"
                    className={`diff-line diff-${kind}`}
                    aria-label={kind === "context" ? undefined : `${kind}: ${text}`}
                  >
                    {line}
                  </div>
                );
              })}
            </div>
          ) : (
            <p className="muted">(no change)</p>
          )}
        </>
      )}
    </div>
  );
}
