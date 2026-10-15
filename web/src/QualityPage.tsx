import { useCallback, useEffect, useState } from "react";
import { get, post, resource } from "./api";
import PhaseHeader from "./PhaseHeader";
import { CopyCommand, ToneChip } from "./GraphBits";
import { enabledRows, gateLabel, offDimensions, statusLabel, statusTone, type QualityCheck, type QualityScorecard } from "./quality";
import { checkTone, citationText, dimensionTitle, historyByDimension, historyStrip, sortedChecks, type HistoryResult } from "./qualityHistory";
import { lineageHref } from "./lineage";

// QualityPage is #/i/<id>/quality, the full form of the QualityScorecard card
// on Health and Build: per dimension, its mode (gate/advisory/off) and status
// (pass/warn/fail/not measured/stale), every check with its method and
// file:line citations, the requirements it is evidence for (each linked to
// its lineage), and its measurement history from GET .../quality/history.
// Measure starts the same measure_quality operation `idea verify` does.
export default function QualityPage({ projectId, initiativeId }: { projectId: string; initiativeId: string }) {
  const [sc, setSc] = useState<QualityScorecard | null>(null);
  const [history, setHistory] = useState<HistoryResult[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const [s, h] = await Promise.all([
        get<QualityScorecard>(resource(projectId, initiativeId, "quality")),
        get<HistoryResult[]>(resource(projectId, initiativeId, "quality/history")).catch(() => []),
      ]);
      setSc(s);
      setHistory(h ?? []);
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
  }, [load]);

  // The measurement is an operation; re-read until nothing needs measuring
  // (bounded, like the card's).
  const measure = async () => {
    setBusy(true);
    setError("");
    try {
      await post(resource(projectId, initiativeId, "quality"), {});
      for (let i = 0; i < 120; i++) {
        await new Promise((r) => setTimeout(r, 1500));
        const next = await get<QualityScorecard>(resource(projectId, initiativeId, "quality"));
        setSc(next);
        if (!next.needs_measurement) break;
      }
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const hist = historyByDimension(history);

  return (
    <div className="phase-page quality-page">
      <PhaseHeader
        title="Quality"
        purpose="Every quality dimension, how it was judged, and where the evidence is."
        meta={sc ? `rigor ${sc.rigor}` : undefined}
      />
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {!sc && !error && <p className="muted">Loading the scorecard…</p>}
      {sc && (
        <>
          <section className="health-card">
            <div className="health-row">
              <span className={`health-sev ${sc.gate_unmet.length ? "sev-high" : "sev-observation"}`}>gate</span>
              <div className="health-body">
                <div className="health-title">{gateLabel(sc)}</div>
                <div className="health-sub">
                  Gated dimensions hold the verification gate; advisory ones only report. Unmeasured or stale is never a
                  pass.
                </div>
              </div>
              <button type="button" className="jc-primary" disabled={busy} onClick={measure}>
                {busy ? "Measuring…" : sc.needs_measurement ? "Measure" : "Re-measure"}
              </button>
            </div>
          </section>

          {enabledRows(sc).map((d) => {
            const h = hist[d.dimension] ?? [];
            const strip = historyStrip(h);
            return (
              <section key={d.dimension} className="qp-dim" aria-labelledby={`qp-${d.dimension}`}>
                <div className="qp-head">
                  <h2 id={`qp-${d.dimension}`} className="qp-title">
                    {dimensionTitle(d.dimension)}
                  </h2>
                  <span className={`qp-mode qp-mode-${d.mode}`}>{d.mode}</span>
                  <ToneChip tone={statusTone(d)}>{statusLabel(d)}</ToneChip>
                  {sc.overridden.includes(d.dimension) && <span className="muted">set in harness.yaml</span>}
                </div>
                {!d.result ? (
                  <p className="muted">Never measured — Measure (or <code>idea verify</code>) measures it.</p>
                ) : (
                  <>
                    <p className="health-sub">
                      {d.result.summary}
                      {d.stale && <strong> Stale: measured against different applied work or policy.</strong>}
                    </p>
                    {d.result.requirements.length > 0 && (
                      <p className="qp-reqs">
                        Evidence for{" "}
                        {d.result.requirements.map((c, i) => (
                          <span key={c}>
                            {i > 0 && ", "}
                            <a href={lineageHref(initiativeId, "requirement", c)}>{c}</a>
                          </span>
                        ))}
                        {" · "}
                        <a href={lineageHref(initiativeId, "quality_result", d.result.id)}>result lineage</a>
                      </p>
                    )}
                    {(() => {
                      // "Nothing to judge" checks fold into a disclosure so
                      // the ones that judged something stay in view.
                      const all = sortedChecks(d.result.checks);
                      const judged = all.filter((c) => c.status !== "not_applicable");
                      const na = all.filter((c) => c.status === "not_applicable");
                      return (
                        <>
                          <ul className="qp-checks">
                            {judged.map((c, i) => (
                              <CheckRow key={`${c.name}-${i}`} c={c} initiativeId={initiativeId} />
                            ))}
                            {all.length === 0 && <li className="muted">No checks recorded.</li>}
                          </ul>
                          {na.length > 0 && (
                            <details className="qp-na">
                              <summary>
                                {na.length} not applicable check{na.length === 1 ? "" : "s"}
                              </summary>
                              <ul className="qp-checks">
                                {na.map((c, i) => (
                                  <CheckRow key={`${c.name}-${i}`} c={c} initiativeId={initiativeId} />
                                ))}
                              </ul>
                            </details>
                          )}
                        </>
                      );
                    })()}
                  </>
                )}
                {strip.length > 0 && (
                  <div className="qp-history">
                    <span className="muted">History ({h.length}):</span>
                    <ol className="qp-strip" aria-label={`${dimensionTitle(d.dimension)} measurement history, oldest first`}>
                      {strip.map((s) => (
                        <li key={s.id} className={`qp-tick quality-${s.tone}`} title={`${s.status}${s.at ? ` · ${new Date(s.at).toLocaleString()}` : ""}`}>
                          <span className="sr-only">
                            {s.status}
                            {s.at ? ` on ${new Date(s.at).toLocaleString()}` : ""}
                          </span>
                        </li>
                      ))}
                    </ol>
                  </div>
                )}
              </section>
            );
          })}
          {offDimensions(sc).length > 0 && (
            <p className="muted qp-offline">
              Off under the current policy (not measured): {offDimensions(sc).map(dimensionTitle).join(", ")}. Turn one on
              with <code>verify.dimensions.&lt;dimension&gt;: advisory</code> in harness.yaml.
            </p>
          )}
          {enabledRows(sc).length === 0 && <p className="muted">Every dimension is off, so nothing is measured.</p>}
          <p className="muted gv-foot">
            <CopyCommand command="idea verify" label="Measure from the terminal:" />
          </p>
        </>
      )}
    </div>
  );
}

// CheckRow is one check: its status, name and method (deterministic or
// model), detail, and citations -- file:line as code, a bare requirement as
// a link to its lineage.
function CheckRow({ c, initiativeId }: { c: QualityCheck; initiativeId: string }) {
  return (
    <li className="qp-check">
      <ToneChip tone={checkTone(c.status)}>{c.status.replace(/_/g, " ")}</ToneChip>
      <div className="qp-check-body">
        <div>
          {c.name} <span className="qp-method">{c.method}</span>
        </div>
        {c.detail && <div className="health-sub qp-detail">{c.detail}</div>}
        {(c.citations ?? []).length > 0 && (
          <div className="qp-cites">
            {(c.citations ?? []).map((ct, j) =>
              ct.requirement && !ct.path ? (
                <a key={j} href={lineageHref(initiativeId, "requirement", ct.requirement)}>
                  {ct.requirement}
                </a>
              ) : (
                <code key={j}>{citationText(ct)}</code>
              ),
            )}
          </div>
        )}
      </div>
    </li>
  );
}
