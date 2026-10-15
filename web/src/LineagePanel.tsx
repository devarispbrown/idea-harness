import { useState } from "react";
import { get, resource } from "./api";
import { NodeLink } from "./GraphBits";
import { directEdges, lineageHref, stalenessCounts, uniqueNodes, type TraceResponse } from "./lineage";

// LineagePanel is the compact "Why / Impact" disclosure embedded wherever a
// graph node is listed (requirement rows on Define, task rows on Plan, change
// sets on Build, the decision page, objectives and metrics). It fetches
// nothing until it is opened -- a page of forty task rows must not fire
// forty trace requests -- and then shows the first ring of each direction
// with a link to the full LineageView.
export default function LineagePanel({
  projectId,
  initiativeId,
  type,
  id,
  code,
}: {
  projectId: string;
  initiativeId: string;
  type: string;
  id: string;
  // The node's human code (REQ-3, TASK-2) for the link and the label; the id
  // is used when there is none.
  code?: string;
}) {
  const [trace, setTrace] = useState<TraceResponse | null>(null);
  const [error, setError] = useState("");

  const onToggle = async (open: boolean) => {
    if (!open || trace) return;
    try {
      const q = new URLSearchParams({ type, id });
      const t = await get<TraceResponse>(`${resource(projectId, initiativeId, "trace")}?${q}`);
      setTrace({ why: t?.why ?? [], impact: t?.impact ?? [] });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const why = trace ? directEdges(trace.why, type, id, "why") : [];
  const impact = trace ? directEdges(trace.impact, type, id, "impact") : [];
  const stale = trace ? stalenessCounts(trace.impact) : {};
  const total = trace ? uniqueNodes(trace.impact, "impact", `${type}:${id}`) : 0;

  return (
    <details className="lp" onToggle={(e) => onToggle((e.currentTarget as HTMLDetailsElement).open)}>
      <summary className="lp-summary">
        Why / Impact<span className="sr-only"> of {code ?? id}</span>
      </summary>
      <div className="lp-body">
        {!trace && !error && <span className="muted">Loading…</span>}
        {error && <span className="error">{error}</span>}
        {trace && (
          <>
            <div className="lp-line">
              <span className="lp-key">Why</span>
              {why.length === 0 ? (
                <span className="muted">no recorded source</span>
              ) : (
                <span className="lp-nodes">
                  {why.slice(0, 6).map((e, i) => (
                    <span key={i} className="lp-node">
                      <span className="lv-edge">{e.edge_type.replace(/_/g, " ")}</span>
                      <NodeLink initiativeId={initiativeId} type={e.to_type} id={e.to_id} label={e.to_label} />
                    </span>
                  ))}
                  {why.length > 6 && <span className="muted">+{why.length - 6} more</span>}
                </span>
              )}
            </div>
            <div className="lp-line">
              <span className="lp-key">Impact</span>
              {impact.length === 0 ? (
                <span className="muted">nothing depends on it</span>
              ) : (
                <span className="lp-nodes">
                  {impact.slice(0, 6).map((e, i) => (
                    <span key={i} className="lp-node">
                      <NodeLink initiativeId={initiativeId} type={e.from_type} id={e.from_id} label={e.from_label} />
                      {e.staleness === "stale" && <span className="lv-stale lv-stale-stale">stale</span>}
                    </span>
                  ))}
                  {impact.length > 6 && <span className="muted">+{impact.length - 6} more</span>}
                </span>
              )}
            </div>
            <div className="lp-line lp-foot">
              <span className="muted">
                {total} transitive dependent{total === 1 ? "" : "s"}
                {stale.stale ? ` · ${stale.stale} stale` : ""}
              </span>
              <a href={lineageHref(initiativeId, type, code ?? id)}>Full lineage →</a>
            </div>
          </>
        )}
      </div>
    </details>
  );
}
