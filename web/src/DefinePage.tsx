import { useCallback, useEffect, useState } from "react";
import { get, post, resource } from "./api";
import PhasePage from "./PhasePage";
import ObjectivesSection from "./ObjectivesSection";
import { ARTIFACT_TITLES } from "./phases";
import { navigate } from "./router";
import type { StatusPill } from "./PhaseHeader";
import type { DocumentRowProps } from "./DocumentRow";
import {
  DecisionCard,
  FindingCard,
  QuestionCard,
  type DecisionItem,
  type FindingItem,
  type QuestionItem,
} from "./JudgmentCard";
import { decisionsForDefine, decisionTypeLabel, findingsForDefine, otherOpenFindings } from "./judgmentScope";
import { openItemsNote, showsRevisePrd } from "./judgmentCopy";
import { optionalLabel, partitionByTriage, type TriageItemLike } from "./triage";

interface Artifact {
  id: string;
  kind: string;
  version: number;
}
interface SuggestedAnswer {
  question_id: string;
  answer: string;
}

export default function DefinePage({
  projectId,
  initiativeId,
  status,
  judgmentReason,
}: {
  projectId: string;
  initiativeId: string;
  status: string;
  // Why the initiative is waiting, from App. Undefined never shows "Revise
  // PRD": showsRevisePrd fails closed to anything but prd_review.
  judgmentReason?: string;
}) {
  const [decisions, setDecisions] = useState<DecisionItem[]>([]);
  const [findings, setFindings] = useState<FindingItem[]>([]);
  const [questions, setQuestions] = useState<QuestionItem[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [suggested, setSuggested] = useState<Record<string, string>>({});
  // B4: null while triage hasn't loaded (or the daemon doesn't have the
  // endpoint yet) -- distinct from "loaded, empty", so the fallback flat
  // rendering below only kicks in when triage is genuinely unavailable.
  const [triage, setTriage] = useState<TriageItemLike[] | null>(null);
  // B4: "Accept all recommendations" -- resolved count and skipped
  // code+reason pairs from the last call, shown until the next load replaces
  // them. Reasons (review #2, ICP re-run) let a human see WHY a decision
  // with options was left open -- no recommendation, no unique match, or a
  // race with a concurrent resolve -- instead of a bare, uninformative count.
  const [acceptResult, setAcceptResult] = useState<{
    resolved: number;
    skipped: { code: string; reason: string }[];
  } | null>(null);
  const [acceptError, setAcceptError] = useState("");

  const load = useCallback(async () => {
    const [d, f, q, a, s, t] = await Promise.all([
      get<DecisionItem[]>(resource(projectId, initiativeId, "decisions")).catch(() => []),
      get<FindingItem[]>(resource(projectId, initiativeId, "findings")).catch(() => []),
      get<QuestionItem[]>(resource(projectId, initiativeId, "questions")).catch(() => []),
      get<Artifact[]>(resource(projectId, initiativeId, "artifacts")).catch(() => []),
      get<SuggestedAnswer[]>(resource(projectId, initiativeId, "suggested-answers")).catch(() => []),
      get<{ items: TriageItemLike[] }>(resource(projectId, initiativeId, "triage")).catch(() => null),
    ]);
    setDecisions(d ?? []);
    setFindings(f ?? []);
    setQuestions(q ?? []);
    setArtifacts(a ?? []);
    const map: Record<string, string> = {};
    for (const sa of s ?? []) if (sa.question_id) map[sa.question_id] = sa.answer;
    setSuggested(map);
    setTriage(t?.items ?? null);
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
    const id = setInterval(load, 2500);
    return () => clearInterval(id);
  }, [load]);

  // D1: Define is the PRD phase, so it shows PRD decisions and PRD-review
  // (+ deterministic requirement-check) findings only. Plan-review findings
  // — including HIGH ones like "No execution plan ... was provided" — belong
  // on Plan, where approval actually happens (see PlanPage.tsx).
  const prdDecisions = decisionsForDefine(decisions);
  const prdFindings = findingsForDefine(findings);
  // C2: the catch-all — a finding whose target_type matches neither Define's
  // nor Plan's allowlist still has to land somewhere, so it shows here rather
  // than being silently dropped (the reported incident: a hidden CRITICAL
  // finding that blocked plan approval with no visible reason).
  const otherFindings = otherOpenFindings(findings).filter((f) => !f.disposition);

  const openDecisions = prdDecisions.filter((d) => d.status === "open");
  const openFindings = prdFindings.filter((f) => !f.disposition);
  const openQuestions = questions.filter((q) => q.status === "open");
  const openCount = openDecisions.length + openFindings.length + openQuestions.length;

  const resolvedDecisions = prdDecisions.filter((d) => d.status !== "open");
  const resolvedFindings = prdFindings.filter((f) => !!f.disposition);

  // B4 triage: cross-reference this phase's own open items (still scoped by
  // judgmentScope.ts above -- triage ranks the WHOLE initiative, so this
  // does not undo that scoping) against the top-3 `priority` ids. Falls back
  // to showing everything under "Needs you" (no collapse) when triage
  // hasn't loaded or the daemon doesn't have the endpoint yet -- degrade to
  // the page's pre-wave-3 behavior, never to an empty list.
  const triageItems = triage ?? [];
  const decisionsSplit = partitionByTriage(openDecisions, triageItems);
  const findingsSplit = partitionByTriage(openFindings, triageItems);
  const questionsSplit = partitionByTriage(openQuestions, triageItems);
  const hasTriage = triage !== null;
  const optionalCount =
    decisionsSplit.optional.length + findingsSplit.optional.length + questionsSplit.optional.length;

  // The current (latest) PRD version; /artifacts returns oldest-first.
  const prdVersions = artifacts.filter((a) => a.kind === "prd");
  const prdVersion = prdVersions.length ? Math.max(...prdVersions.map((a) => a.version)) : 0;
  const prd = prdVersions.find((a) => a.version === prdVersion);
  const nextVersion = prdVersion + 1;
  const sortedPrd = [...prdVersions].sort((a, b) => a.version - b.version);
  const prevPrd = sortedPrd.length >= 2 ? sortedPrd[sortedPrd.length - 2] : undefined;

  const resolveDecision = async (id: string, resolution: string) => {
    await post(`/projects/${projectId}/decisions/${id}/resolve`, { resolution });
    await load();
  };
  const reopenDecision = async (id: string) => {
    await post(`/projects/${projectId}/decisions/${id}/reopen`, {});
    await load();
  };
  const dispositionFinding = async (id: string, disposition: string) => {
    await post(`/projects/${projectId}/findings/${id}/disposition`, { disposition });
    await load();
  };
  const answerQuestion = async (id: string, answer: string) => {
    await post(`/projects/${projectId}/questions/${id}/answer`, { answer });
    await load();
  };
  // B4: "Accept all recommendations" -- answers the ICP round's "53 things
  // need your judgment" in one call. Confirms first (this resolves every
  // open decision on the initiative, not only Define's), then shows what
  // was resolved vs. skipped (an open decision with no recommendation to
  // accept).
  const acceptAllRecommendations = async () => {
    if (
      !window.confirm(
        `Accept the recommendation for every open decision on this initiative? This cannot be undone.`,
      )
    ) {
      return;
    }
    setAcceptError("");
    setAcceptResult(null);
    try {
      const res = await post<{
        resolved: string[];
        skipped: { code: string; reason: string }[];
      }>(resource(projectId, initiativeId, "decisions/accept-recommendations"), {});
      setAcceptResult({ resolved: (res.resolved ?? []).length, skipped: res.skipped ?? [] });
      await load();
    } catch (e) {
      setAcceptError(e instanceof Error ? e.message : String(e));
    }
  };

  const suggest = async () => {
    await post(`/projects/${projectId}/initiatives/${initiativeId}/suggest-answers`, {});
    await new Promise((res) => setTimeout(res, 1500));
    await load();
  };
  const revise = async () => {
    await post(`/projects/${projectId}/initiatives/${initiativeId}/revise`, {});
    await load();
  };
  const advance = async () => {
    await post(`/projects/${projectId}/initiatives/${initiativeId}/advance`, {});
    await load();
  };

  const docs: DocumentRowProps[] = prd
    ? [
        {
          title: `${ARTIFACT_TITLES.prd} · v${prd.version}`,
          meta: "",
          onRead: () => navigate(`#/i/${initiativeId}/doc/${prd.id}/${prd.version}`),
          ...(prevPrd
            ? {
                onDiff: () => navigate(`#/i/${initiativeId}/diff/${prevPrd.id}/${prd.id}`),
                diffLabel: `Diff v${prevPrd.version} → v${prd.version}`,
              }
            : {}),
        },
      ]
    : [];

  const pill: StatusPill | undefined =
    status === "running"
      ? { label: "running", state: "running" }
      : status === "needs_human_judgment"
        ? { label: "waiting on you", state: "needs-you" }
        : status === "approved"
          ? { label: "approved", state: "verified" }
          : undefined;

  // The advancing action must stay live through the whole phase (rule 2/3): the
  // bar never vanishes while judgment is still the next step.
  let commit: { explanation: string; actionLabel: string; onAction: () => void } | undefined;
  if (status === "approved") {
    commit = {
      explanation: "The PRD is reviewed and agreed.",
      actionLabel: "Proceed to design & plan →",
      onAction: advance,
    };
  } else if (status === "needs_human_judgment" && showsRevisePrd(judgmentReason)) {
    // D2: the four judgment reasons share this one status, and only
    // prd_review (or the undefined legacy case) is Define's to resolve — a
    // plan_review/plan_dependencies/verification park shows no commit bar
    // here at all, because "Revise PRD" would not be the right action.
    commit = {
      explanation:
        openCount > 0
          ? `${openCount} unresolved will use the recommendation.`
          : "All resolved — regenerate the PRD.",
      actionLabel: `Revise PRD to v${nextVersion} →`,
      onAction: revise,
    };
  }

  return (
    <PhasePage title="Define" purpose="Turning research into an agreed, reviewed PRD." pill={pill} docs={docs} commit={commit}>
      {openCount > 0 && (
        <div className="judgment-head">
          <h2 className="section-title">
            {openCount} thing{openCount === 1 ? "" : "s"} need{openCount === 1 ? "s" : ""} your judgment
          </h2>
          <span className="muted">{openItemsNote(status, nextVersion, judgmentReason)}</span>
        </div>
      )}
      {/* B4: "53 things need your judgment", then 82. Repeated questions. No
          way to accept the recommendations." One call resolves every open
          decision on the initiative with its own recommendation. */}
      {openCount > 0 && (
        <div className="row" style={{ marginBottom: 12 }}>
          <button className="jc-secondary" onClick={acceptAllRecommendations}>
            Accept all recommendations
          </button>
        </div>
      )}
      {acceptResult && (
        <div className="ok-text">
          <p>
            Resolved {acceptResult.resolved}, skipped {acceptResult.skipped.length}
            {acceptResult.skipped.length > 0 ? ":" : "."}
          </p>
          {acceptResult.skipped.length > 0 && (
            <ul className="accept-recommendations-skipped">
              {acceptResult.skipped.map((s) => (
                <li key={s.code}>
                  {s.code}: {s.reason}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {acceptError && <p className="error">{acceptError}</p>}
      <div className="judgment-list">
        {decisionsSplit.needsYou.map((d) => (
          <DecisionCard key={d.id} d={d} onResolve={(r) => resolveDecision(d.id, r)} onReopen={() => reopenDecision(d.id)} projectId={projectId} initiativeId={initiativeId} typeLabel={decisionTypeLabel(d.type)} />
        ))}
        {findingsSplit.needsYou.map((f) => (
          <FindingCard key={f.id} f={f} onDisposition={(dis) => dispositionFinding(f.id, dis)} />
        ))}
        {questionsSplit.needsYou.map((q) => (
          <QuestionCard key={q.id} q={q} suggested={suggested[q.id]} onAnswer={(a) => answerQuestion(q.id, a)} onSuggest={suggest} />
        ))}
      </div>
      {/* Triage's collapsed remainder (B4): shown open, uncollapsed, only
          while triage hasn't loaded yet -- otherwise a fresh page load would
          hide every open item behind a closed <details> for a moment. */}
      {optionalCount > 0 && (
        <details className="w3-triage-optional" open={!hasTriage}>
          <summary>{optionalLabel(optionalCount)}</summary>
          <div className="judgment-list">
            {decisionsSplit.optional.map((d) => (
              <DecisionCard key={d.id} d={d} onResolve={(r) => resolveDecision(d.id, r)} onReopen={() => reopenDecision(d.id)} projectId={projectId} initiativeId={initiativeId} typeLabel={decisionTypeLabel(d.type)} />
            ))}
            {findingsSplit.optional.map((f) => (
              <FindingCard key={f.id} f={f} onDisposition={(dis) => dispositionFinding(f.id, dis)} />
            ))}
            {questionsSplit.optional.map((q) => (
              <QuestionCard key={q.id} q={q} suggested={suggested[q.id]} onAnswer={(a) => answerQuestion(q.id, a)} onSuggest={suggest} />
            ))}
          </div>
        </details>
      )}
      {(resolvedDecisions.length > 0 || resolvedFindings.length > 0) && (
        <>
          <div className="judgment-head">
            <h2 className="section-title">Resolved</h2>
          </div>
          <div className="judgment-list">
            {resolvedDecisions.map((d) => (
              <DecisionCard key={d.id} d={d} onResolve={() => {}} onReopen={() => reopenDecision(d.id)} projectId={projectId} initiativeId={initiativeId} typeLabel={decisionTypeLabel(d.type)} />
            ))}
            {resolvedFindings.map((f) => (
              <FindingCard key={f.id} f={f} onDisposition={() => {}} />
            ))}
          </div>
        </>
      )}
      {/* C2: nothing may be silently unshown. A finding whose target_type is
          not yet recognized by either page's allowlist still shows up here,
          rather than vanishing the way the reported CRITICAL finding did. */}
      {otherFindings.length > 0 && (
        <>
          <div className="judgment-head">
            <h2 className="section-title">Other open items</h2>
            <span className="muted">
              didn't match a known phase — shown here so nothing is hidden
            </span>
          </div>
          <div className="judgment-list">
            {otherFindings.map((f) => (
              <FindingCard key={f.id} f={f} onDisposition={(dis) => dispositionFinding(f.id, dis)} />
            ))}
          </div>
        </>
      )}
      {/* The outcome end of the chain: what this PRD's requirements are for,
          and the metrics that say whether they worked. */}
      <ObjectivesSection projectId={projectId} initiativeId={initiativeId} />
    </PhasePage>
  );
}
