import { useCallback, useEffect, useState } from "react";
import { get, post, resource } from "./api";
import {
  attentionChecks,
  citationLabel,
  enabledRows,
  gateLabel,
  offDimensions,
  statusLabel,
  statusTone,
  type QualityScorecard as Scorecard,
} from "./quality";

// QualityScorecard is the small quality-dimension card shown on the Health
// and Build pages: one row per enabled dimension with its mode and status,
// the checks that need attention with their file:line citations, and a
// Measure button (POST .../quality). It renders nothing until the scorecard
// has loaded, and nothing at all when every dimension is off.
export default function QualityScorecard({
  projectId,
  initiativeId,
}: {
  projectId: string;
  initiativeId: string;
}) {
  const [sc, setSc] = useState<Scorecard | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setSc(await get<Scorecard>(resource(projectId, initiativeId, "quality")).catch(() => null));
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
  }, [load]);

  // The measurement runs as an operation; the scorecard is re-read until it no
  // longer needs measuring (or a bounded number of polls passes).
  const measure = async () => {
    setBusy(true);
    setError("");
    try {
      await post(resource(projectId, initiativeId, "quality"), {});
      for (let i = 0; i < 120; i++) {
        await new Promise((r) => setTimeout(r, 2000));
        const next = await get<Scorecard>(resource(projectId, initiativeId, "quality"));
        setSc(next);
        if (!next.needs_measurement) break;
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  if (!sc) return null;
  const rows = enabledRows(sc);
  if (rows.length === 0) return null;
  const off = offDimensions(sc);

  return (
    <>
      <div className="doc-eyebrow" style={{ margin: "24px 0 10px" }}>
        Quality · rigor {sc.rigor}
        <a className="gv-eyebrow-link" href={`#/i/${initiativeId}/quality`}>
          Full scorecard and history →
        </a>
      </div>
      <section className="health-card">
        <div className="health-row">
          <span className={`health-sev ${sc.gate_unmet.length ? "sev-high" : "sev-observation"}`}>gate</span>
          <div className="health-body">
            <div className="health-title">{gateLabel(sc)}</div>
            <div className="health-sub">
              Unmeasured is shown as not measured, never as a pass. Only gated dimensions hold the verification gate.
            </div>
          </div>
          <button className="cs-secondary" disabled={busy} onClick={measure}>
            {busy ? "Measuring…" : sc.needs_measurement ? "Measure" : "Re-measure"}
          </button>
        </div>
        {rows.map((r) => (
          <div key={r.dimension} className="health-row">
            <span className={`health-sev quality-${statusTone(r)}`}>{statusLabel(r)}</span>
            <div className="health-body">
              <div className="health-title">
                {r.dimension} <span className="muted">· {r.mode}</span>
              </div>
              <div className="health-sub">{r.result?.summary ?? "Never measured — idea verify measures it."}</div>
              {attentionChecks(r.result).map((c) => (
                <div key={c.name} className="health-sub">
                  <span className="muted">[{c.method}]</span> {c.name}
                  {citationLabel(c) && <code> {citationLabel(c)}</code>}
                  {c.detail && <span className="muted"> — {c.detail.split("\n")[0]}</span>}
                </div>
              ))}
              {r.result && r.result.requirements.length > 0 && (
                <div className="health-sub muted">evidence for {r.result.requirements.join(", ")}</div>
              )}
            </div>
          </div>
        ))}
        {off.length > 0 && (
          <div className="muted" style={{ padding: "8px 16px" }}>
            off: {off.join(", ")}
          </div>
        )}
        {error && <p className="error">{error}</p>}
      </section>
    </>
  );
}
