import { useCallback, useEffect, useState } from "react";
import { get, post, resource } from "./api";
import PhaseHeader from "./PhaseHeader";

interface Criterion {
  metric: string;
  target: string;
  condition: string;
}
interface Requirement {
  id: string;
  code: string;
  statement: string;
  status: string;
  acceptance_criteria: Criterion[];
}

export default function VerifyPage({
  projectId,
  initiativeId,
}: {
  projectId: string;
  initiativeId: string;
}) {
  const [reqs, setReqs] = useState<Requirement[]>([]);
  const [note, setNote] = useState<Record<string, string>>({});
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const r = await get<Requirement[]>(resource(projectId, initiativeId, "requirements")).catch(() => []);
    setReqs(r ?? []);
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
    const id = setInterval(load, 2500);
    return () => clearInterval(id);
  }, [load]);

  const pass = async (code: string) => {
    setError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/verify`, {
        requirement: code,
        pass: true,
        note: note[code]?.trim() || undefined,
      });
      setNote((p) => ({ ...p, [code]: "" }));
      await load();
    } catch (e) {
      setError(String(e));
    }
  };

  const activeReqs = reqs.filter((r) => r.status !== "removed");
  const totalCriteria = activeReqs.reduce((n, r) => n + r.acceptance_criteria.length, 0);
  const verifiedReqs = activeReqs.filter((r) => r.status === "verified").length;
  const passedCriteria = activeReqs
    .filter((r) => r.status === "verified")
    .reduce((n, r) => n + r.acceptance_criteria.length, 0);
  const pct = totalCriteria > 0 ? Math.round((passedCriteria / totalCriteria) * 100) : 0;

  return (
    <div className="phase-page">
      <PhaseHeader title="Verify" purpose="Record evidence that each requirement's acceptance criteria hold." />

      <div className="verify-meter">
        <div className="verify-track">
          <span className="verify-fill" style={{ width: `${pct}%` }} />
        </div>
        <span className="verify-meta">
          {passedCriteria} of {totalCriteria} criteria · {verifiedReqs} of {activeReqs.length} requirements verified
        </span>
      </div>

      <div className="verify-list">
        {activeReqs.map((r) => {
          const verified = r.status === "verified";
          return (
            <section key={r.id} className={`verify-card ${verified ? "" : "current"}`}>
              <div className="verify-head">
                <span className="verify-mark">{verified ? "✓" : "▶"}</span>
                <span className="verify-code">{r.code}</span>
                <span className="verify-statement">{r.statement}</span>
                {verified && <span className="phase-pill pill-verified">verified</span>}
                <span className="verify-count">
                  {verified ? r.acceptance_criteria.length : 0}/{r.acceptance_criteria.length}
                </span>
              </div>
              <div className="verify-criteria">
                {r.acceptance_criteria.map((c, i) => (
                  <div key={i} className="verify-criterion">
                    <span className="verify-criterion-mark">{verified ? "✓" : "○"}</span>
                    <span className="verify-criterion-text">
                      {c.metric} {c.target} ({c.condition})
                    </span>
                  </div>
                ))}
              </div>
              {!verified && r.acceptance_criteria.length > 0 && (
                <div className="verify-pass">
                  <input
                    className="text-input"
                    placeholder="Evidence note — what you checked, and how"
                    value={note[r.code] ?? ""}
                    onChange={(e) => setNote((p) => ({ ...p, [r.code]: e.target.value }))}
                  />
                  <button className="cs-green" onClick={() => pass(r.code)}>Pass</button>
                </div>
              )}
            </section>
          );
        })}
      </div>
      {reqs.length === 0 && <p className="muted">No requirements to verify yet.</p>}
      {error && <p className="error" style={{ marginTop: 12 }}>{error}</p>}
    </div>
  );
}
