import { useCallback, useEffect, useState } from "react";
import { get, post, resource } from "./api";
import PhaseHeader from "./PhaseHeader";
import {
  confirmableRequirementCodes,
  type EvidenceTest,
  evidenceSummary,
  hasBuiltSomething,
  hasConfirmableEvidence,
  stripSourceTag,
  verifiedRequirementCount,
} from "./verify";

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
  // change_kind (brownfield delta): "added" (default), "modified" (revised in
  // place), or "removed" (superseded; filtered out of the active list).
  change_kind?: string;
  acceptance_criteria: Criterion[];
  // evidence_tests (task 7, C2 wire contract): the test names mapped to this
  // requirement and whether each passed. Optional so this still works
  // against a daemon that hasn't shipped it yet.
  evidence_tests?: EvidenceTest[];
}
interface ChangeSetStatus {
  status: string;
  // no_op (H8 fix): a no-op applied change set touched nothing -- hasBuiltSomething
  // (verify.ts) reads this so "built" is never true from one alone.
  no_op?: boolean;
}

export default function VerifyPage({
  projectId,
  initiativeId,
}: {
  projectId: string;
  initiativeId: string;
}) {
  const [reqs, setReqs] = useState<Requirement[]>([]);
  const [changeSets, setChangeSets] = useState<ChangeSetStatus[]>([]);
  const [note, setNote] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [confirmBusy, setConfirmBusy] = useState(false);
  // Nothing about "built" is known before the first load; without this the
  // gate note flashed on a page whose change set had long been applied.
  const [loaded, setLoaded] = useState(false);

  const load = useCallback(async () => {
    const [r, c] = await Promise.all([
      get<Requirement[]>(resource(projectId, initiativeId, "requirements")).catch(() => []),
      get<ChangeSetStatus[]>(resource(projectId, initiativeId, "change-sets")).catch(() => []),
    ]);
    setReqs(r ?? []);
    setChangeSets(c ?? []);
    setLoaded(true);
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
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  // confirmPassingTests (task 7) posts the human sign-off for every
  // criterion whose mapped tests all passed -- a passing check is evidence,
  // but a human still confirms it; nothing is auto-verified. H1: a
  // confirmation dialog names exactly which requirement codes are about to
  // flip, so this is never a surprise one-click action.
  const confirmPassingTests = async () => {
    if (
      !window.confirm(
        `Confirm ${confirmCandidates.length} requirement${confirmCandidates.length === 1 ? "" : "s"} whose tests all passed?\n\n${confirmCandidates.join(", ")}`,
      )
    ) {
      return;
    }
    setError("");
    setConfirmBusy(true);
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/verify`, {
        confirm_passing_tests: true,
      });
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setConfirmBusy(false);
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
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const activeReqs = reqs.filter((r) => r.status !== "removed");
  // Waived requirements are out of scope: they do not count toward the
  // verifiable criteria or the "requirements verified" denominator.
  const verifiableReqs = activeReqs.filter((r) => r.status !== "waived");
  const waivedReqs = activeReqs.filter((r) => r.status === "waived").length;
  const totalCriteria = verifiableReqs.reduce((n, r) => n + r.acceptance_criteria.length, 0);
  // Same function App.tsx's sidebar uses on the same requirements shape, so
  // the two numbers can't drift apart again (K).
  const verifiedReqs = verifiedRequirementCount(reqs);
  const passedCriteria = activeReqs
    .filter((r) => r.status === "verified")
    .reduce((n, r) => n + r.acceptance_criteria.length, 0);
  const pct = totalCriteria > 0 ? Math.round((passedCriteria / totalCriteria) * 100) : 0;
  // Pass/Fail record evidence against code that landed in the workspace --
  // before anything has been built and applied, there is nothing to check
  // yet (K).
  const built = hasBuiltSomething(changeSets);
  // task 7 / H1: only offered when it would actually confirm something -- a
  // passing test somewhere doesn't mean every requirement has one -- AND
  // once something has actually been built and applied. Manual Pass/Fail
  // already gates on `built` for exactly this reason: confirming against
  // code that never landed is meaningless, whatever the check output says.
  const confirmCandidates = confirmableRequirementCodes(verifiableReqs);
  // hasConfirmableEvidence (LOW fix, release-gate report): this predicate had
  // zero production callers -- the button was gated on
  // `confirmCandidates.length > 0` instead, which is the same rule
  // (isConfirmCandidate) computed twice. Wired in here rather than deleted:
  // it's the clearer, named form of exactly the check this page needs.
  const canConfirmPassingTests = built && hasConfirmableEvidence(verifiableReqs);

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

      {loaded && !built && verifiableReqs.length > 0 && (
        <p className="muted verify-gate-note">
          Pass and Fail appear once a task has been built and its change set applied — there's
          nothing to check against yet. Waive is available now.
        </p>
      )}

      {/* task 7: the human sign-off, labelled as exactly that -- passing
          tests are evidence, but this button is what records a human
          confirming it, not an automatic verification. */}
      {canConfirmPassingTests && (
        <div className="verify-confirm-all">
          <button className="cs-green" disabled={confirmBusy} onClick={confirmPassingTests}>
            {confirmBusy ? "Confirming…" : "Confirm all with passing tests"}
          </button>
          <span className="muted verify-confirm-all-note">
            Human sign-off: confirms every criterion whose mapped tests all passed.
          </span>
        </div>
      )}

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
                <span className="verify-statement">{stripSourceTag(r.statement)}</span>
                {verified && <span className="phase-pill pill-verified">verified</span>}
                {failed && <span className="phase-pill pill-failing">failed</span>}
                {waived && <span className="phase-pill pill-inactive">waived</span>}
                {r.change_kind === "modified" && <span className="phase-pill pill-modified">modified</span>}
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
              {/* task 7: evidence_tests per requirement -- a passing check is
                  evidence, shown before the human decides anything.
                  Criteria with no passing-test evidence at all keep the
                  manual Pass/Fail below with nothing extra to show here. */}
              {evidenceSummary(r.evidence_tests) && (
                <p className="verify-evidence">{evidenceSummary(r.evidence_tests)}</p>
              )}
              {!verified && !waived && r.acceptance_criteria.length > 0 && (
                <div className="verify-pass">
                  <input
                    className="text-input"
                    aria-label={`Evidence note for ${r.code}`}
                    placeholder={built ? "Evidence note — what you checked, and how" : "Note (optional) — e.g. why it is waived"}
                    value={note[r.code] ?? ""}
                    onChange={(e) => setNote((p) => ({ ...p, [r.code]: e.target.value }))}
                  />
                  {/* Pass/Fail only exist once there is something to check: the
                      page used to render them (disabled) right under a note
                      saying they were locked, which read as a contradiction --
                      and a disabled button explains nothing on its own. */}
                  {built && (
                    <button className="cs-green" onClick={() => record(r.code, "pass")}>
                      Pass
                    </button>
                  )}
                  {built && !failed && (
                    <button className="cs-danger" onClick={() => record(r.code, "fail")}>
                      Fail
                    </button>
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
