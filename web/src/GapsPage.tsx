import { useCallback, useEffect, useState } from "react";
import { get, post, resource } from "./api";
import PhaseHeader from "./PhaseHeader";
import { CopyCommand, NodeLink } from "./GraphBits";
import { gapSummaryLine, gapSubject, type Gap, type GapReport } from "./gaps";
import { SEVERITIES, countBy, filterGaps, gapAction, gapKindTitle, groupGapsByKind } from "./gapsView";
import { isLineageType } from "./lineage";

// GapsPage is #/i/<id>/gaps: the full GET .../gaps report (what `idea gaps`
// prints), grouped by kind and filterable by kind and severity. Each gap shows
// its why, the nodes it is about (each linked to its lineage), and the action
// that closes it -- a button where this UI can do it, otherwise the CLI
// command with a copy button. The Health page keeps a top-8 card that links
// here.
export default function GapsPage({ projectId, initiativeId }: { projectId: string; initiativeId: string }) {
  const [report, setReport] = useState<GapReport | null>(null);
  const [error, setError] = useState("");
  const [kind, setKind] = useState("");
  const [severity, setSeverity] = useState("");
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");

  const load = useCallback(async () => {
    try {
      setReport(await get<GapReport>(resource(projectId, initiativeId, "gaps")));
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
  }, [load]);

  // Records a human judgment on an assumption -- the one gap kind this page
  // closes in place (POST /assertions/{id}/status).
  const judge = async (assertionId: string, status: "accepted" | "disputed" | "retracted") => {
    setBusy(assertionId);
    setNotice("");
    try {
      await post(`/projects/${encodeURIComponent(projectId)}/assertions/${encodeURIComponent(assertionId)}/status`, { status });
      setNotice(`Assumption ${assertionId.slice(0, 8)} marked ${status}.`);
      await load();
    } catch (e) {
      setNotice(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy("");
    }
  };

  const gaps = report?.gaps ?? [];
  const shown = filterGaps(gaps, { kind, severity });
  const groups = groupGapsByKind(shown);
  const kindCounts = countBy(gaps, "kind");
  const sevCounts = countBy(gaps, "severity");

  return (
    <div className="phase-page gaps-page">
      <PhaseHeader
        title="Gaps"
        purpose="Every hole in the Product Graph, and what closes it."
      />
      {report && (
        <p className="muted gv-summary">
          {report.summary?.total ?? gaps.length} total
          {gaps.length ? ` · ${gapSummaryLine(report.summary?.by_severity)}` : ""}
        </p>
      )}
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {!report && !error && <p className="muted">Loading gaps…</p>}

      {report && gaps.length > 0 && (
        <form className="gv-filters" onSubmit={(e) => e.preventDefault()} aria-label="Filter gaps">
          <label>
            Kind{" "}
            <select className="text-input" value={kind} onChange={(e) => setKind(e.target.value)}>
              <option value="">All kinds ({gaps.length})</option>
              {Object.keys(kindCounts)
                .sort()
                .map((k) => (
                  <option key={k} value={k}>
                    {gapKindTitle(k)} ({kindCounts[k]})
                  </option>
                ))}
            </select>
          </label>
          <label>
            Severity{" "}
            <select className="text-input" value={severity} onChange={(e) => setSeverity(e.target.value)}>
              <option value="">All severities</option>
              {[...SEVERITIES, ...Object.keys(sevCounts).filter((s) => !SEVERITIES.includes(s))]
                .filter((s) => sevCounts[s])
                .map((s) => (
                  <option key={s} value={s}>
                    {s} ({sevCounts[s]})
                  </option>
                ))}
            </select>
          </label>
          {(kind || severity) && (
            <button
              type="button"
              className="cs-secondary"
              onClick={() => {
                setKind("");
                setSeverity("");
              }}
            >
              Clear filters
            </button>
          )}
          <span className="muted" aria-live="polite">
            Showing {shown.length} of {gaps.length}
          </span>
        </form>
      )}
      {notice && (
        <p className="ok-text" role="status">
          {notice}
        </p>
      )}

      {report && gaps.length === 0 && (
        <section className="health-card">
          <div className="muted" style={{ padding: "13px 16px" }}>
            No gaps: every in-scope requirement is implemented and verified.
          </div>
        </section>
      )}

      {groups.map((g) => (
        <section key={g.kind} className="gv-group" aria-labelledby={`gap-${g.kind}`}>
          <h2 id={`gap-${g.kind}`} className="section-title">
            {g.title} <span className="muted">· {g.gaps.length}</span>
          </h2>
          <ul className="health-card gv-list">
            {g.gaps.map((gap, i) => (
              <GapRow
                key={`${gap.state}-${gapSubject(gap)}-${i}`}
                gap={gap}
                initiativeId={initiativeId}
                busy={busy}
                onJudge={judge}
              />
            ))}
          </ul>
        </section>
      ))}
      <p className="muted gv-foot">
        The same report: <code>idea gaps</code> (or <code>idea gaps --json</code> with every node and id).
      </p>
    </div>
  );
}

function GapRow({
  gap,
  initiativeId,
  busy,
  onJudge,
}: {
  gap: Gap;
  initiativeId: string;
  busy: string;
  onJudge: (id: string, s: "accepted" | "disputed" | "retracted") => void;
}) {
  const action = gapAction(gap, initiativeId);
  return (
    <li className="health-row gv-row">
      <span className={`health-sev sev-${gap.severity}`}>{gap.severity}</span>
      <div className="health-body">
        <div className="health-title">{gap.why}</div>
        <div className="gv-nodes">
          {(gap.nodes ?? []).map((n, i) =>
            isLineageType(n.type) ? (
              <NodeLink
                key={i}
                initiativeId={initiativeId}
                type={n.type}
                id={n.id}
                // A coded node (REQ-3) leads with its code; an assertion's
                // "code" is only its short id, which NodeLink derives itself.
                label={/^[A-Z][A-Z0-9]*-\d+/.test(n.code) ? `${n.code}${n.label ? ` ${n.label}` : ""}` : n.label}
              />
            ) : (
              <span key={i} className="gv-node gv-node-plain">
                <span className="gv-node-code">{n.code}</span>
                {n.label && <span className="gv-node-text">{n.label}</span>}
              </span>
            ),
          )}
          {gap.dependents > 0 && (
            <span className="muted">
              · {gap.dependents} dependent{gap.dependents === 1 ? "" : "s"}
            </span>
          )}
          <span className="muted">· {gap.state.replace(/_/g, " ")}</span>
        </div>
        <div className="gv-action">
          {action.kind === "link" && (
            <a className="jc-secondary gv-action-btn" href={action.href}>
              {action.label} →
            </a>
          )}
          {action.kind === "assertion" && (
            <span className="gv-judge" role="group" aria-label="Record your judgment on this assumption">
              {(["accepted", "disputed", "retracted"] as const).map((s) => (
                <button
                  key={s}
                  type="button"
                  className={s === "accepted" ? "jc-primary" : "jc-secondary"}
                  disabled={busy === action.assertionId}
                  onClick={() => onJudge(action.assertionId, s)}
                >
                  {s === "accepted" ? "Accept" : s === "disputed" ? "Dispute" : "Retract"}
                </button>
              ))}
            </span>
          )}
          {gap.next && <CopyCommand command={gap.next} label={action.kind === "cli" ? "Close it with" : "or"} />}
        </div>
      </div>
    </li>
  );
}
