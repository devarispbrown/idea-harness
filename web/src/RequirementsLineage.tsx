import { useEffect, useState } from "react";
import { get, resource } from "./api";
import LineagePanel from "./LineagePanel";
import { lineageHref } from "./lineage";

interface Requirement {
  id: string;
  code: string;
  statement: string;
  status: string;
  priority?: string;
  // change_kind (brownfield delta): "added" (default), "modified" (revised in
  // place), or "removed" (superseded; filtered out here).
  change_kind?: string;
}

// RequirementsLineage lists the PRD's live requirements on Define, each with
// its Why / Impact disclosure -- where a requirement came from, and the tasks,
// change sets, tests and metrics that hang off it. The PRD document itself
// is prose; this is the graph view of the same requirements.
export default function RequirementsLineage({ projectId, initiativeId }: { projectId: string; initiativeId: string }) {
  const [reqs, setReqs] = useState<Requirement[]>([]);

  useEffect(() => {
    let live = true;
    get<Requirement[]>(resource(projectId, initiativeId, "requirements"))
      .then((r) => live && setReqs(r ?? []))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [projectId, initiativeId]);

  const shown = reqs.filter((r) => r.status !== "removed" && r.status !== "superseded");
  if (shown.length === 0) return null;

  return (
    <section aria-labelledby="rl-head">
      <div className="judgment-head">
        <h2 id="rl-head" className="section-title">
          Requirements
        </h2>
        <span className="muted">
          {shown.length} in the graph — open one to see why it exists and what depends on it
        </span>
      </div>
      <ul className="health-card gv-list">
        {shown.map((r) => (
          <li key={r.id} className="health-row rl-row">
            <a className="idchip chip-accent" href={lineageHref(initiativeId, "requirement", r.code)}>
              {r.code}
            </a>
            <div className="health-body">
              <div className="health-title">{r.statement}</div>
              <div className="health-sub">
                {r.priority ? `${r.priority} · ` : ""}
                {r.status.replace(/_/g, " ")}
                {r.change_kind === "modified" && <span className="phase-pill pill-modified">modified</span>}
              </div>
              <LineagePanel projectId={projectId} initiativeId={initiativeId} type="requirement" id={r.id} code={r.code} />
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}
