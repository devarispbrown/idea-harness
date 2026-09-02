import { useCallback, useEffect, useState } from "react";
import { get, post } from "./api";
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
    const r = await get<Requirement[]>(`/projects/${projectId}/requirements?initiative_id=${initiativeId}`).catch(() => []);
    setReqs(r ?? []);
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
    const id = setInterval(load, 2500);
    return () => clearInterval(id);
  }, [load]);

  const record = async (code: string, disposition: "pass" | "fail" | "waived") => {
    setError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/verify`, {
        requirement: code,
        pass: disposition === "pass",
        fail: disposition === "fail",
        waived: disposition === "waived",
        note: note[code]?.trim() || undefined,
      });
      setNote((p) => ({ ...p, [code]: "" }));
      await load();
    } catch (e) {
      setError(String(e));
    }
  };

  const generateTask = async (r: Requirement) => {
    setError("");
    try {
      await post(
        `/projects/${projectId}/initiatives/${initiativeId}/requirements/${r.id}/verification-task`,
        {},
      );
      await load();
    } catch (e) {
      setError(String(e));
    }
  };

  const activeReqs = reqs.filter((r) => r.status !== "removed");
  // Waived requirements are out of scope: they do not count toward the
  // verifiable criteria or the "requirements verified" denominator.
  const verifiableReqs = activeReqs.filter((r) => r.status !== "waived");
  const waivedReqs = activeReqs.filter((r) => r.status === "waived").length;
  const totalCriteria = verifiableReqs.reduce((n, r) => n + r.acceptance_criteria.length, 0);
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
          {passedCriteria} of {totalCriteria} criteria · {verifiedReqs} of {verifiableReqs.length} requirements verified
          {waivedReqs > 0 ? ` · ${waivedReqs} waived` : ""}
        </span>
      </div>

      <div className="verify-list">
        {activeReqs.map((r) => {
          const verified = r.status === "verified";
          const failed = r.status === "failed";
          const waived = r.status === "waived";
          return (
            <section key={r.id} className={`verify-card ${verified || waived ? "" : "current"}`}>
              <div className="verify-head">
                <span className="verify-mark">{verified ? "✓" : waived ? "—" : "▶"}</span>
                <span className="verify-code">{r.code}</span>
                <span className="verify-statement">{r.statement}</span>
                {verified && <span className="phase-pill pill-verified">verified</span>}
                {failed && <span className="phase-pill pill-failing">failed</span>}
                {waived && <span className="phase-pill pill-inactive">waived</span>}
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
              {!verified && !waived && r.acceptance_criteria.length > 0 && (
                <div className="verify-pass">
                  <input
                    className="text-input"
                    placeholder="Evidence note — what you checked, and how"
                    value={note[r.code] ?? ""}
                    onChange={(e) => setNote((p) => ({ ...p, [r.code]: e.target.value }))}
                  />
                  <button className="cs-green" onClick={() => record(r.code, "pass")}>Pass</button>
                  {!failed && (
                    <button className="cs-danger" onClick={() => record(r.code, "fail")}>Fail</button>
                  )}
                  <button className="cs-secondary" onClick={() => record(r.code, "waived")}>Waive</button>
                  <button className="verify-task" onClick={() => generateTask(r)}>Add verify task</button>
                </div>
              )}
              {waived && (
                <div className="verify-pass">
                  <button className="cs-secondary" onClick={() => record(r.code, "pass")}>Un-waive (pass)</button>
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
