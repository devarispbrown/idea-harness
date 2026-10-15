import { useEffect, useMemo, useState } from "react";
import { get, resource } from "./api";
import PhaseHeader from "./PhaseHeader";
import { CopyCommand, NodeLink } from "./GraphBits";
import {
  LINEAGE_TYPES,
  LIST_RESOURCE,
  buildLineageTree,
  isLineageType,
  nodeTypeLabel,
  normalizeNodeType,
  pageHrefFor,
  resolveRef,
  stalenessCounts,
  uniqueNodes,
  type LineageTreeNode,
  type ResolvedNode,
  type TraceResponse,
} from "./lineage";

// LineageView is #/i/<id>/lineage/<type>/<ref>: one Product Graph node's
// provenance (why it exists, following its edges up to their sources), its
// impact (everything that depends on it, with the daemon's staleness
// classification) and the full edge trace -- the same answers `idea why`,
// `idea impact` and `idea trace` give, from the same GET .../trace. `ref` is
// a code (REQ-3, CHG-1, a dimension name, an artifact kind) or an id,
// resolved the way the CLI resolves it.
export default function LineageView({
  projectId,
  initiativeId,
  nodeType,
  nodeRefParam,
}: {
  projectId: string;
  initiativeId: string;
  nodeType: string;
  nodeRefParam: string;
}) {
  const type = normalizeNodeType(nodeType);
  const [node, setNode] = useState<ResolvedNode | null>(null);
  const [trace, setTrace] = useState<TraceResponse | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let live = true;
    setLoading(true);
    setError("");
    setTrace(null);
    setNode(null);
    (async () => {
      if (!isLineageType(type)) {
        setError(`"${nodeType}" is not a graph node type. Use one of: ${LINEAGE_TYPES.join(", ")}.`);
        return;
      }
      let resolved: ResolvedNode | null = null;
      const list = LIST_RESOURCE[type];
      if (list) {
        const rows = await get<Record<string, unknown>[]>(resource(projectId, initiativeId, list)).catch(() => null);
        resolved = rows ? resolveRef(type, nodeRefParam, rows) : null;
        if (!resolved) {
          if (live) setError(`No ${nodeTypeLabel(type).toLowerCase()} matching "${nodeRefParam}" in this initiative.`);
          return;
        }
      } else {
        resolved = { id: nodeRefParam, code: nodeRefParam.slice(0, 8), title: nodeTypeLabel(type), raw: {} };
      }
      const q = new URLSearchParams({ type, id: resolved.id });
      const t = await get<TraceResponse>(`${resource(projectId, initiativeId, "trace")}?${q}`);
      if (!live) return;
      setNode(resolved);
      setTrace({ why: t?.why ?? [], impact: t?.impact ?? [] });
    })()
      .catch((e) => live && setError(e instanceof Error ? e.message : String(e)))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [projectId, initiativeId, type, nodeType, nodeRefParam]);

  const rootKey = node ? `${type}:${node.id}` : "";
  const whyTree = useMemo(
    () => (node && trace ? buildLineageTree(type, node.id, trace.why, "why") : []),
    [node, trace, type],
  );
  const impactTree = useMemo(
    () => (node && trace ? buildLineageTree(type, node.id, trace.impact, "impact") : []),
    [node, trace, type],
  );
  const stale = trace ? stalenessCounts(trace.impact) : {};
  const openHref = node ? pageHrefFor(initiativeId, type, node) : undefined;
  const cliRef = node?.code && node.code !== node.id.slice(0, 8) ? node.code : node?.id ?? nodeRefParam;

  return (
    <div className="phase-page lineage-page">
      <nav className="lv-crumbs" aria-label="Breadcrumb">
        <a href={`#/i/${initiativeId}/health`}>Health</a>
        <span aria-hidden="true"> / </span>
        <a href={`#/i/${initiativeId}/gaps`}>Gaps</a>
        <span aria-hidden="true"> / </span>
        <span>Lineage</span>
      </nav>
      <PhaseHeader
        title={node ? `${node.code}${node.title ? ` · ${node.title}` : ""}` : `${nodeTypeLabel(type)} ${nodeRefParam}`}
        purpose={`${nodeTypeLabel(type)} lineage — why it exists, and what moves if it changes.`}
        meta={node?.status}
      />
      {loading && !error && <p className="muted">Loading lineage…</p>}
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}

      {node && trace && (
        <>
          <div className="lv-actions">
            {openHref && (
              <a className="cs-secondary lv-open" href={openHref}>
                Open where it lives →
              </a>
            )}
            <CopyCommand command={`idea trace ${type} ${cliRef}`} />
          </div>

          <section className="lv-section" aria-labelledby="lv-why">
            <h2 id="lv-why" className="section-title">
              Why <span className="muted">· provenance, {uniqueNodes(trace.why, "why", rootKey)} upstream</span>
            </h2>
            {whyTree.length === 0 ? (
              <p className="muted">Nothing upstream — this node was not derived from anything the graph records.</p>
            ) : (
              <LineageTree initiativeId={initiativeId} nodes={whyTree} direction="why" />
            )}
          </section>

          <section className="lv-section" aria-labelledby="lv-impact">
            <h2 id="lv-impact" className="section-title">
              Impact <span className="muted">· {uniqueNodes(trace.impact, "impact", rootKey)} dependent{uniqueNodes(trace.impact, "impact", rootKey) === 1 ? "" : "s"}</span>
            </h2>
            {Object.keys(stale).length > 0 && (
              <p className="lv-stale-summary">
                {Object.entries(stale).map(([s, n]) => (
                  <span key={s} className={`lv-stale lv-stale-${s}`}>
                    {n} {s === "revalidate" ? "to revalidate" : s}
                  </span>
                ))}
              </p>
            )}
            {impactTree.length === 0 ? (
              <p className="muted">Nothing depends on this yet.</p>
            ) : (
              <LineageTree initiativeId={initiativeId} nodes={impactTree} direction="impact" />
            )}
          </section>

          <details className="lv-section lv-trace">
            <summary>
              Full trace · {trace.why.length + trace.impact.length} edge{trace.why.length + trace.impact.length === 1 ? "" : "s"}
            </summary>
            <table className="lv-table">
              <caption className="sr-only">Every edge in this node's provenance and impact</caption>
              <thead>
                <tr>
                  <th scope="col">From</th>
                  <th scope="col">Edge</th>
                  <th scope="col">To</th>
                  <th scope="col">Staleness</th>
                </tr>
              </thead>
              <tbody>
                {[...trace.why, ...trace.impact].map((e, i) => (
                  <tr key={i}>
                    <td>
                      <NodeLink initiativeId={initiativeId} type={e.from_type} id={e.from_id} label={e.from_label} />
                    </td>
                    <td className="lv-edge">{e.edge_type.replace(/_/g, " ")}</td>
                    <td>
                      <NodeLink initiativeId={initiativeId} type={e.to_type} id={e.to_id} label={e.to_label} />
                    </td>
                    <td>{"staleness" in e ? (e as { staleness: string }).staleness : "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </details>
        </>
      )}
    </div>
  );
}

// LineageTree renders a tree as nested lists of links: plain lists rather
// than an ARIA tree widget, so every node is an ordinary tab stop and the
// nesting is announced by the list semantics alone.
function LineageTree({
  initiativeId,
  nodes,
  direction,
}: {
  initiativeId: string;
  nodes: LineageTreeNode[];
  direction: "why" | "impact";
}) {
  return (
    <ul className="lv-tree">
      {nodes.map((n, i) => (
        <li key={`${n.key}-${i}`} className="lv-item">
          <span className="lv-row">
            <span className="lv-edge" title={direction === "why" ? "edge to this source" : "edge from this dependent"}>
              {n.edge.replace(/_/g, " ")}
            </span>
            <NodeLink initiativeId={initiativeId} type={n.type} id={n.id} label={n.label} showType />
            {/* "revalidate" is the default classification of every live
                dependent; badging each row with it buried the stale and
                obsolete ones. The summary above still counts it. */}
            {n.staleness && n.staleness !== "revalidate" && (
              <span className={`lv-stale lv-stale-${n.staleness}`}>{n.staleness}</span>
            )}
            {n.repeat && <span className="muted lv-repeat">(shown above)</span>}
          </span>
          {n.children.length > 0 && <LineageTree initiativeId={initiativeId} nodes={n.children} direction={direction} />}
        </li>
      ))}
    </ul>
  );
}
