import { useCallback, useEffect, useState } from "react";
import { get, resource } from "./api";
import PhaseHeader from "./PhaseHeader";
import QualityScorecard from "./QualityScorecard";

interface RiskProfile {
  tier: string;
  signals: { name: string; detected: boolean }[];
  reason: string[];
}
interface Finding {
  id: string;
  severity: string;
  category: string;
  finding: string;
  recommendation: string;
}
interface Health {
  status: string;
  version: string;
}

export default function HealthPage({
  projectId,
  initiativeId,
}: {
  projectId: string;
  initiativeId: string;
}) {
  const [risk, setRisk] = useState<RiskProfile | null>(null);
  const [doctor, setDoctor] = useState<Finding[]>([]);
  const [health, setHealth] = useState<Health | null>(null);

  const load = useCallback(async () => {
    const [r, d, h] = await Promise.all([
      get<RiskProfile>(resource(projectId, initiativeId, "risk")).catch(() => null),
      get<Finding[]>(resource(projectId, initiativeId, "doctor")).catch(() => []),
      get<Health>("/health").catch(() => null),
    ]);
    setRisk(r);
    setDoctor(d ?? []);
    setHealth(h);
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <div className="phase-page">
      <PhaseHeader title="Graph health" purpose="Whether the provenance you're relying on is actually complete." />

      {risk && (
        <section className="risk-card">
          <div className="risk-head">
            <span className="risk-pill">risk · {risk.tier}</span>
            <div className="risk-body">
              <div className="risk-title">
                {risk.reason?.[0] ?? (risk.signals?.some((s) => s.detected) ? "Risk signals detected in the applied patches" : "No risk signals detected")}
              </div>
              <div className="risk-sub">Derived deterministically from the patch contents — no model, no score.</div>
            </div>
          </div>
          <div className="risk-signals">
            {(risk.signals ?? []).filter((s) => s.detected).map((s) => (
              <span key={s.name} className="risk-signal">{s.name}</span>
            ))}
          </div>
        </section>
      )}

      <div className="doc-eyebrow" style={{ margin: "24px 0 10px" }}>
        Traceability gaps · {doctor.length}
      </div>
      <section className="health-card">
        {doctor.map((d) => (
          <div key={d.id} className="health-row">
            <span className={`health-sev sev-${d.severity}`}>{d.severity}</span>
            <div className="health-body">
              <div className="health-title">{d.finding}</div>
              {d.recommendation && <div className="health-sub">{d.recommendation}</div>}
            </div>
          </div>
        ))}
        {doctor.length === 0 && <div className="muted" style={{ padding: "13px 16px" }}>No gaps.</div>}
      </section>

      <QualityScorecard projectId={projectId} initiativeId={initiativeId} />

      <div className="doc-eyebrow" style={{ margin: "24px 0 10px" }}>Daemon</div>
      <section className="health-daemon">
        <span className={`health-dot ${health?.status === "ok" ? "" : "down"}`} />
        <span className="health-addr">
          {/* No hardcoded version fallback. This read "v0.1.0", frozen since the
              file was written, so a daemon that had not answered yet rendered a
              version three releases old as though it were the running build --
              the same defect as docs/openapi.yaml's info.version, in the UI. An
              em dash says "not known yet", which is what is true.
              The address used to be hard-coded to 127.0.0.1:8787, wrong
              whenever the UI is reached through a different host/port (a
              tunnel, --addr, a reverse proxy) -- location.host is what's
              actually true here. The state path is also dropped rather than
              asserted: this UI has no endpoint that reports it, and
              ~/.local/share/idea-harness is only the default, not a
              guarantee. */}
          {window.location.host} · {health?.version ?? "—"}
        </span>
      </section>
    </div>
  );
}
