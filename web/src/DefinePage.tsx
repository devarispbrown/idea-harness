import { useCallback, useEffect, useState } from "react";
import { get, post, resource } from "./api";
import PhasePage from "./PhasePage";
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
}: {
  projectId: string;
  initiativeId: string;
  status: string;
}) {
  const [decisions, setDecisions] = useState<DecisionItem[]>([]);
  const [findings, setFindings] = useState<FindingItem[]>([]);
  const [questions, setQuestions] = useState<QuestionItem[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [suggested, setSuggested] = useState<Record<string, string>>({});

  const load = useCallback(async () => {
    const [d, f, q, a, s] = await Promise.all([
      get<DecisionItem[]>(resource(projectId, initiativeId, "decisions")).catch(() => []),
      get<FindingItem[]>(resource(projectId, initiativeId, "findings")).catch(() => []),
      get<QuestionItem[]>(resource(projectId, initiativeId, "questions")).catch(() => []),
      get<Artifact[]>(resource(projectId, initiativeId, "artifacts")).catch(() => []),
      get<SuggestedAnswer[]>(resource(projectId, initiativeId, "suggested-answers")).catch(() => []),
    ]);
    setDecisions(d ?? []);
    setFindings(f ?? []);
    setQuestions(q ?? []);
    setArtifacts(a ?? []);
    const map: Record<string, string> = {};
    for (const sa of s ?? []) if (sa.question_id) map[sa.question_id] = sa.answer;
    setSuggested(map);
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
    const id = setInterval(load, 2500);
    return () => clearInterval(id);
  }, [load]);

  const openDecisions = decisions.filter((d) => d.status === "open");
  const openFindings = findings.filter((f) => !f.disposition);
  const openQuestions = questions.filter((q) => q.status === "open");
  const openCount = openDecisions.length + openFindings.length + openQuestions.length;

  const resolvedDecisions = decisions.filter((d) => d.status !== "open");
  const resolvedFindings = findings.filter((f) => !!f.disposition);

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
  } else if (status === "needs_human_judgment") {
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
          <span className="muted">then the PRD revises to v{nextVersion} and Design unlocks</span>
        </div>
      )}
      <div className="judgment-list">
        {openDecisions.map((d) => (
          <DecisionCard key={d.id} d={d} onResolve={(r) => resolveDecision(d.id, r)} onReopen={() => reopenDecision(d.id)} projectId={projectId} initiativeId={initiativeId} />
        ))}
        {openFindings.map((f) => (
          <FindingCard key={f.id} f={f} onDisposition={(dis) => dispositionFinding(f.id, dis)} />
        ))}
        {openQuestions.map((q) => (
          <QuestionCard key={q.id} q={q} suggested={suggested[q.id]} onAnswer={(a) => answerQuestion(q.id, a)} onSuggest={suggest} />
        ))}
      </div>
      {(resolvedDecisions.length > 0 || resolvedFindings.length > 0) && (
        <>
          <div className="judgment-head">
            <h2 className="section-title">Resolved</h2>
          </div>
          <div className="judgment-list">
            {resolvedDecisions.map((d) => (
              <DecisionCard key={d.id} d={d} onResolve={() => {}} onReopen={() => reopenDecision(d.id)} projectId={projectId} initiativeId={initiativeId} />
            ))}
            {resolvedFindings.map((f) => (
              <FindingCard key={f.id} f={f} onDisposition={() => {}} />
            ))}
          </div>
        </>
      )}
    </PhasePage>
  );
}
