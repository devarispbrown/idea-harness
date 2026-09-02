import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { get, post, apiRaw, uploadDoc, token } from "./api";
import Settings from "./Settings";
import { changeStatusClass, dispositionClass, dispositionLabel } from "./statusChips";

interface Project {
  id: string;
  name: string;
  project_id: string;
  root_path: string;
}
interface Initiative {
  id: string;
  title: string;
  description: string;
  status: string;
}
interface UnderstandingItem {
  id: string;
  type: string;
  status: string;
  statement: string;
  confidence: string;
  source: string;
}
interface Requirement {
  id: string;
  code: string;
  statement: string;
  priority: string;
  acceptance_criteria: { metric: string; target: string; condition: string }[];
}
interface Dissent {
  proponent_role: string;
  position: string;
}
interface Decision {
  id: string;
  code: string;
  question: string;
  recommendation: string;
  options: string[];
  resolution: string;
  status: string;
  has_dissent?: boolean;
  dissents?: Dissent[];
}
interface Task {
  id: string;
  code: string;
  title: string;
  statement: string;
  status: string;
  priority: string;
  estimate: string;
}
interface TraceItem {
  edge_type: string;
  from_type: string;
  from_id: string;
  from_label: string;
  to_type: string;
  to_id: string;
  to_label: string;
}
interface ImpactItem extends TraceItem {
  staleness: string;
}
interface SuggestedAnswer {
  question_id: string;
  question: string;
  answer: string;
}
interface DecisionDiscussion {
  id: string;
  decision_id: string;
  author: string;
  message: string;
  created_at: string;
}
interface Question {
  id: string;
  question: string;
  status: string;
}
interface Finding {
  id: string;
  severity: string;
  category: string;
  finding: string;
  recommendation: string;
  evidence: string;
  impact: string;
  disposition: string;
  note?: string;
}
interface Understanding {
  title: string;
  description: string;
  status: string;
  failure_reason: string;
  summary: string;
  needs_judgment: string[];
  assertions: UnderstandingItem[];
  requirements: Requirement[];
  decisions: Decision[];
  questions: Question[];
}
interface NextMove {
  action: string;
  why: string;
  affected: string[];
}
interface ChangeGroup {
  category: string;
  items: string[];
}
interface Changed {
  groups: ChangeGroup[];
}
interface Artifact {
  id: string;
  kind: string;
  version: number;
}
interface ChangeSet {
  id: string;
  human_id: string;
  initiative_id?: string;
  task_id?: string;
  base_snapshot_id?: string;
  status: string;
  patch_digest: string;
  review_id?: string;
  git_commit_sha?: string;
  created_by_run_id?: string;
  created_at?: string;
}

interface VerifyCoverage {
  code: string;
  pass: number;
  total: number;
  needs_manual: number;
}
interface VerifyResponse {
  status: string;
  coverage: VerifyCoverage[];
}

interface ContextDoc {
  id: string;
  initiative_id: string;
  kind: string;
  title: string;
  digest: string;
  preview: string;
  format?: string;
  description_digest?: string;
  created_at: string;
}

interface Command {
  label: string;
  hint: string;
  run: () => void;
}

// CollapsibleList shows the first `limit` items with a "Show all" toggle, to
// keep dense sections (assumptions, questions, findings) readable (§79).
function CollapsibleList<T>({
  items,
  limit = 5,
  render,
}: {
  items: T[];
  limit?: number;
  render: (item: T, index: number) => ReactNode;
}) {
  const [expanded, setExpanded] = useState(false);
  const shown = expanded ? items : items.slice(0, limit);
  return (
    <>
      {shown.map((item, i) => render(item, i))}
      {items.length > limit && (
        <button className="ghost small" onClick={() => setExpanded((v) => !v)}>
          {expanded ? "Show less" : `Show all ${items.length}`}
        </button>
      )}
    </>
  );
}

// eventLabel maps a daemon event to a human phrase for the live feed (§42).
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function eventLabel(e: any): string {
  const t = e.event_type ?? "";
  switch (t) {
    case "initiative.created":
      return "Initiative created";
    case "initiative.status_changed":
      return "Workflow advanced";
    case "artifact.created":
      return "Artifact created";
    case "assertion.created":
      return "Assertion recorded";
    case "requirement.created":
      return "Requirement added";
    case "requirements.superseded":
      return "Requirements revised after review";
    case "decision.created":
      return "Decision raised";
    case "decision.resolved":
      return "Decision resolved";
    case "question.created":
      return "Open question added";
    case "question.answered":
      return "Question answered";
    case "finding.created":
      return "Review finding raised";
    case "finding.dispositioned":
      return "Finding dispositioned";
    case "run.created":
      return "Model request started";
    case "run.status_changed":
      return "Model request finished";
    case "operation.created":
      return "Operation started";
    case "operation.status_changed":
      return "Operation finished";
    default:
      return t || (e.aggregate_type ?? "");
  }
}

// dispositionLabel, dispositionClass, and changeStatusClass are pure helpers
// extracted to ./statusChips so they can be unit-tested without a DOM.

export default function App() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [projectId, setProjectId] = useState<string>("");
  const [initiatives, setInitiatives] = useState<Initiative[]>([]);
  const [initiative, setInitiative] = useState<Initiative | null>(null);
  const [understanding, setUnderstanding] = useState<Understanding | null>(null);
  const [nextMove, setNextMove] = useState<NextMove | null>(null);
  const [changed, setChanged] = useState<Changed | null>(null);
  const [findings, setFindings] = useState<Finding[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [artifactView, setArtifactView] = useState<{ kind: string; body: string } | null>(null);
  const [demo, setDemo] = useState(false);
  const [idea, setIdea] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [view, setView] = useState<"ideas" | "settings">("ideas");
  const [creating, setCreating] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [liveLog, setLiveLog] = useState<{ seq: number; text: string }[]>([]);
  const [logs, setLogs] = useState<string[]>([]);
  const [answerDrafts, setAnswerDrafts] = useState<Record<string, string>>({});
  const [decisionDrafts, setDecisionDrafts] = useState<Record<string, string>>({});
  const [editingDecision, setEditingDecision] = useState<string | null>(null);
  const [findingNotes, setFindingNotes] = useState<Record<string, string>>({});
  const [discussionThreads, setDiscussionThreads] = useState<Record<string, DecisionDiscussion[]>>({});
  const [discussionDrafts, setDiscussionDrafts] = useState<Record<string, string>>({});
  const [discussingDecision, setDiscussingDecision] = useState<string | null>(null);
  const [discussing, setDiscussing] = useState(false);
  const [revising, setRevising] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [bulkDecisions, setBulkDecisions] = useState<Record<string, string>>({});
  const [bulkFindings, setBulkFindings] = useState<Record<string, string>>({});
  const [bulkQuestions, setBulkQuestions] = useState<Record<string, string>>({});
  const [bulkBusy, setBulkBusy] = useState(false);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [changeSets, setChangeSets] = useState<ChangeSet[]>([]);
  const [advancing, setAdvancing] = useState(false);
  const [executing, setExecuting] = useState(false);
  const [verifying, setVerifying] = useState(false);
  const [verifyStatus, setVerifyStatus] = useState("");
  const [coverage, setCoverage] = useState<VerifyCoverage[]>([]);
  const [verifyNotes, setVerifyNotes] = useState<Record<string, string>>({});
  const [diffView, setDiffView] = useState<{ humanId: string; body: string } | null>(null);
  const [traceModal, setTraceModal] = useState<{ type: string; id: string; label: string } | null>(null);
  const [traceWhy, setTraceWhy] = useState<TraceItem[]>([]);
  const [traceImpact, setTraceImpact] = useState<ImpactItem[]>([]);
  const [docs, setDocs] = useState<ContextDoc[]>([]);
  const [docsKind, setDocsKind] = useState("other");
  const [docsTitle, setDocsTitle] = useState("");
  const [docsPaste, setDocsPaste] = useState("");
  const [docsBusy, setDocsBusy] = useState(false);
  const [suggestingAnswers, setSuggestingAnswers] = useState(false);
  const [notice, setNotice] = useState<{ kind: "success" | "info" | "error"; text: string } | null>(null);
  const noticeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const flash = useCallback((kind: "success" | "info" | "error", text: string) => {
    setNotice({ kind, text });
    if (noticeTimer.current) clearTimeout(noticeTimer.current);
    noticeTimer.current = setTimeout(() => setNotice(null), 6500);
  }, []);
  useEffect(() => () => {
    if (noticeTimer.current) clearTimeout(noticeTimer.current);
  }, []);
  const [tab, setTab] = useState<
    "overview" | "requirements" | "decisions" | "findings" | "questions" | "artifacts" | "tasks" | "changes" | "context" | "activity" | "logs"
  >("overview");

  const loadProjects = useCallback(async () => {
    try {
      const ps = (await get<Project[]>("/projects")) ?? [];
      setProjects(ps);
      if (ps.length > 0) setProjectId(ps[0].project_id);
    } catch (e) {
      setError(String(e));
    }
  }, []);

  useEffect(() => {
    loadProjects();
  }, [loadProjects]);

  const loadInitiatives = useCallback(async (pid: string, selectId?: string) => {
    const list = (await get<Initiative[]>(`/projects/${pid}/initiatives`)) ?? [];
    setInitiatives(list);
    setInitiative((prev) => {
      if (list.length === 0) return null;
      // Explicit selection (new/retry) — jump to that initiative.
      if (selectId) {
        return list.find((i) => i.id === selectId) ?? list[list.length - 1];
      }
      const found = list.find((i) => i.id === prev?.id);
      if (found) {
        // Return the SAME reference unless the status changed, to avoid
        // redundant refetches driven by object churn.
        return found.status === prev?.status ? prev : found;
      }
      return list[list.length - 1]; // newest
    });
  }, []);

  useEffect(() => {
    if (projectId) loadInitiatives(projectId).catch((e) => setError(String(e)));
  }, [projectId, loadInitiatives]);

  // Reset verification UI state when switching initiatives so coverage/badges
  // never leak from a previous initiative (requirement codes collide).
  useEffect(() => {
    setVerifyStatus("");
    setCoverage([]);
    setVerifyNotes({});
  }, [initiative?.id]);

  const loadInitiativeData = useCallback(async (pid: string, ini: Initiative) => {
    const [u, n, c, f, a, t] = await Promise.all([
      get<Understanding>(`/projects/${pid}/initiatives/${ini.id}/understanding`),
      get<NextMove>(`/projects/${pid}/initiatives/${ini.id}/next-move`),
      get<Changed>(`/projects/${pid}/initiatives/${ini.id}/changed`),
      get<Finding[]>(`/projects/${pid}/findings?initiative_id=${ini.id}`),
      get<Artifact[]>(`/projects/${pid}/artifacts`),
      get<Task[]>(`/projects/${pid}/initiatives/${ini.id}/tasks`),
    ]);
    setUnderstanding(u);
    setNextMove(n);
    setChanged(c);
    setFindings(f);
    setArtifacts(a);
    setTasks(t ?? []);
  }, []);

  useEffect(() => {
    if (projectId && initiative) {
      loadInitiativeData(projectId, initiative).catch((e) => setError(String(e)));
    }
  }, [projectId, initiative, loadInitiativeData]);

  const loadLogs = useCallback(async () => {
    try {
      const v = await get<{ lines: string[] }>(`/logs?n=60`);
      setLogs(v.lines ?? []);
    } catch {
      /* ignore */
    }
  }, [projectId]);

  const loadChangeSets = useCallback(
    async (initiativeId?: string) => {
      if (!projectId) return;
      const iniId = initiativeId ?? initiative?.id;
      // Change sets are initiative-scoped; skip the fetch until an initiative is
      // loaded so mount-time effects don't 404 and surface a spurious error.
      if (!iniId) return;
      try {
        const list = (await get<ChangeSet[]>(`/projects/${projectId}/change-sets?initiative_id=${encodeURIComponent(iniId)}`)) ?? [];
        setChangeSets(list);
      } catch (e) {
        setError(String(e));
      }
    },
    [projectId, initiative?.id],
  );

  useEffect(() => {
    if (projectId) loadChangeSets();
  }, [projectId, loadChangeSets]);

  useEffect(() => {
    if (projectId && tab === "changes") loadChangeSets();
  }, [projectId, tab, loadChangeSets]);

  const loadDocs = useCallback(async () => {
    if (!projectId || !initiative?.id) return;
    try {
      const list = (await get<ContextDoc[]>(`/projects/${projectId}/initiatives/${initiative.id}/docs`)) ?? [];
      setDocs(list);
    } catch (e) {
      setError(String(e));
    }
  }, [projectId, initiative?.id]);

  useEffect(() => {
    if (projectId && initiative?.id) loadDocs();
  }, [projectId, initiative?.id, loadDocs]);

  // Demo banner.
  useEffect(() => {
    if (projectId) {
      get<{ demo: boolean }>(`/projects/${projectId}/providers`)
        .then((v) => setDemo(v.demo))
        .catch(() => {});
    }
  }, [projectId]);

  // Daemon logs.
  useEffect(() => {
    if (projectId) loadLogs();
  }, [projectId, loadLogs]);

  // Latest initiative in a ref, so the SSE subscription does not depend on it.
  const initiativeRef = useRef<Initiative | null>(null);
  useEffect(() => {
    initiativeRef.current = initiative;
  }, [initiative]);

  // refresh re-fetches the initiative list, the current initiative's detail, and
  // the daemon log. It is shared by the SSE debounce and the polling fallback so
  // logs and status stay current even if the SSE stream silently drops.
  const refresh = useCallback(() => {
    loadInitiatives(projectId).catch(() => {});
    const ini = initiativeRef.current;
    if (ini) loadInitiativeData(projectId, ini).catch(() => {});
    loadChangeSets().catch(() => {});
    loadLogs();
  }, [projectId, loadInitiatives, loadInitiativeData, loadChangeSets, loadLogs]);

  // Polling fallback: refresh every few seconds regardless of SSE health.
  useEffect(() => {
    if (!projectId) return;
    const id = setInterval(refresh, 2500);
    return () => clearInterval(id);
  }, [projectId, refresh]);

  // Live updates via SSE. The EventSource is opened ONCE per project, and
  // incoming events are debounced into at most one refresh per 500ms, so bursts
  // of events cannot flood the daemon with requests.
  useEffect(() => {
    if (!projectId) return;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const scheduleRefresh = () => {
      if (timer) return;
      timer = setTimeout(() => {
        timer = null;
        refresh();
      }, 500);
    };
    const es = new EventSource(
      `/api/v1/projects/${projectId}/events?token=${encodeURIComponent(token)}`,
    );
    es.onmessage = (ev) => {
      try {
        const e = JSON.parse(ev.data as string);
        setLiveLog((prev) => [
          ...prev.slice(-49),
          { seq: e.seq, text: eventLabel(e) },
        ]);
      } catch {
        /* ignore malformed events */
      }
      scheduleRefresh();
    };
    return () => {
      es.close();
      if (timer) clearTimeout(timer);
    };
  }, [projectId, refresh]);

  // ⌘K / Ctrl+K command palette.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen((v) => !v);
      }
      if (e.key === "Escape") setPaletteOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const createIdea = async () => {
    if (!projectId || !idea.trim()) return;
    setBusy(true);
    setError("");
    try {
      const created = await post<{ initiative_id: string }>(`/projects/${projectId}/initiatives`, {
        title: idea.trim(),
        description: idea.trim(),
      });
      setIdea("");
      setCreating(false);
      await loadInitiatives(projectId, created.initiative_id);
      flash("info", "Exploration started — research and a reviewed PRD are underway.");
    } catch (e) {
      setError(String(e));
      flash("error", `Failed to start: ${e}`);
    } finally {
      setBusy(false);
    }
  };

  const resolveDecision = async (id: string, resolution: string) => {
    try {
      await post(`/projects/${projectId}/decisions/${id}/resolve`, { resolution });
      if (initiative) await loadInitiativeData(projectId, initiative);
      flash("success", "Decision recorded.");
    } catch (e) {
      setError(String(e));
      flash("error", `Failed to resolve decision: ${e}`);
    }
  };

  const saveDecisionEdit = async (id: string) => {
    const resolution = (decisionDrafts[id] ?? "").trim();
    if (!resolution) return;
    setEditingDecision(null);
    setDecisionDrafts((prev) => ({ ...prev, [id]: "" }));
    await resolveDecision(id, resolution);
  };

  const loadDecisionDiscussion = async (id: string) => {
    try {
      const thread = (await get<DecisionDiscussion[]>(`/projects/${projectId}/decisions/${id}/discussion`)) ?? [];
      setDiscussionThreads((prev) => ({ ...prev, [id]: thread }));
    } catch (e) {
      setError(String(e));
    }
  };

  const toggleDiscuss = (id: string) => {
    if (discussingDecision === id) {
      setDiscussingDecision(null);
    } else {
      setDiscussingDecision(id);
      loadDecisionDiscussion(id);
    }
  };

  const sendDecisionDiscussion = async (id: string) => {
    const message = (discussionDrafts[id] ?? "").trim();
    if (!message || discussing) return;
    setDiscussing(true);
    try {
      await post(`/projects/${projectId}/decisions/${id}/discuss`, { message });
      setDiscussionDrafts((prev) => ({ ...prev, [id]: "" }));
      await loadDecisionDiscussion(id);
      flash("success", "Response received.");
    } catch (e) {
      setError(String(e));
      flash("error", `Discuss failed: ${e}`);
    } finally {
      setDiscussing(false);
    }
  };

  const dispositionFinding = async (id: string, disposition: string) => {
    try {
      await post(`/projects/${projectId}/findings/${id}/disposition`, { disposition });
      if (initiative) await loadInitiativeData(projectId, initiative);
      flash("success", "Finding dispositioned.");
    } catch (e) {
      setError(String(e));
      flash("error", `Failed to disposition finding: ${e}`);
    }
  };

  const saveFindingNote = async (id: string) => {
    const note = (findingNotes[id] ?? "").trim();
    if (!note) return;
    try {
      await post(`/projects/${projectId}/findings/${id}/note`, { note });
      setFindingNotes((prev) => ({ ...prev, [id]: "" }));
      if (initiative) await loadInitiativeData(projectId, initiative);
      flash("success", "Note added.");
    } catch (e) {
      setError(String(e));
      flash("error", `Failed to add note: ${e}`);
    }
  };

  const answerQuestion = async (id: string) => {
    const answer = (answerDrafts[id] ?? "").trim();
    if (!answer) return;
    try {
      await post(`/projects/${projectId}/questions/${id}/answer`, { answer });
      setAnswerDrafts((prev) => ({ ...prev, [id]: "" }));
      if (initiative) await loadInitiativeData(projectId, initiative);
      flash("success", "Question answered.");
    } catch (e) {
      setError(String(e));
      flash("error", `Failed to answer question: ${e}`);
    }
  };

  const reviseInitiative = async () => {
    if (!initiative) return;
    setRevising(true);
    setError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiative.id}/revise`, {});
      await loadInitiativeData(projectId, initiative);
      flash("info", "Revision started — re-running product definition & review.");
    } catch (e) {
      setError(String(e));
      flash("error", `Revision failed: ${e}`);
    } finally {
      setRevising(false);
    }
  };

  // advanceInitiative launches the M2 Design → Execution Plan pipeline.
  const advanceInitiative = async () => {
    if (!initiative) return;
    setAdvancing(true);
    setError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiative.id}/advance`, {});
      flash("info", "Advance started — producing design, plan, and review.");
    } catch (e) {
      setError(String(e));
      flash("error", `Advance failed: ${e}`);
    } finally {
      setAdvancing(false);
    }
  };

  // openTrace loads why + impact for a node and opens the panel.
  const openTrace = async (type: string, id: string, label: string) => {
    if (!initiative) return;
    setTraceModal({ type, id, label });
    setTraceWhy([]);
    setTraceImpact([]);
    try {
      const [w, im] = await Promise.all([
        get<TraceItem[]>(`/projects/${projectId}/initiatives/${initiative.id}/why?type=${encodeURIComponent(type)}&id=${encodeURIComponent(id)}`),
        get<ImpactItem[]>(`/projects/${projectId}/initiatives/${initiative.id}/impact?type=${encodeURIComponent(type)}&id=${encodeURIComponent(id)}`),
      ]);
      setTraceWhy(w ?? []);
      setTraceImpact(im ?? []);
    } catch (e) {
      setError(String(e));
    }
  };

  // openTakeRecommendations pre-fills the recommended answer for every open
  // judgment item and opens the confirmation modal for review/editing.
  const openTakeRecommendations = () => {
    const dd: Record<string, string> = {};
    for (const d of understanding?.decisions ?? []) {
      if (d.status === "open") dd[d.id] = d.recommendation || "";
    }
    const fd: Record<string, string> = {};
    for (const f of findings ?? []) {
      if (!f.disposition) fd[f.id] = "mitigate";
    }
    const qd: Record<string, string> = {};
    for (const q of understanding?.questions ?? []) {
      if (q.status === "open") qd[q.id] = "";
    }
    setBulkDecisions(dd);
    setBulkFindings(fd);
    setBulkQuestions(qd);
    setConfirmOpen(true);
    fetchSuggestedAnswers();
  };

  // fetchSuggestedAnswers runs the AI answer generation as a background
  // operation (non-blocking): POST returns an operation id, then we poll the
  // growing partial-answer list (driving real progress) and the operation status.
  const fetchSuggestedAnswers = async () => {
    if (!initiative) return;
    setSuggestingAnswers(true);
    try {
      const { operation_id } = await post<{ operation_id: string }>(
        `/projects/${projectId}/initiatives/${initiative.id}/suggest-answers`,
        {},
      );
      const deadline = Date.now() + 90000;
      while (Date.now() < deadline) {
        await new Promise((res) => setTimeout(res, 1200));
        // Partial answers arrive incrementally (chunked generation) → real progress.
        const partial =
          (await get<SuggestedAnswer[]>(
            `/projects/${projectId}/suggested-answers?initiative_id=${initiative.id}`,
          )) ?? [];
        setBulkQuestions((prev) => {
          const next = { ...prev };
          for (const a of partial) {
            if (a.question_id) next[a.question_id] = a.answer;
          }
          return next;
        });
        const op = await get<{ status: string }>(`/operations/${operation_id}`);
        if (op.status === "succeeded") {
          flash("success", `Answers generated for ${partial.length} question(s).`);
          return;
        }
        if (op.status === "failed") {
          flash("error", "Could not generate answers.");
          return;
        }
      }
      flash("error", "Answer generation timed out.");
    } catch (e) {
      flash("error", `Could not generate answers: ${e}`);
    } finally {
      setSuggestingAnswers(false);
    }
  };

  // submitRecommendations applies the (possibly edited) bulk answers, then
  // triggers the PRD revision.
  const submitRecommendations = async () => {
    if (!initiative) return;
    setBulkBusy(true);
    try {
      for (const d of understanding?.decisions ?? []) {
        if (d.status !== "open") continue;
        const resolution = (bulkDecisions[d.id] ?? "").trim();
        if (resolution) await post(`/projects/${projectId}/decisions/${d.id}/resolve`, { resolution });
      }
      for (const f of findings ?? []) {
        if (f.disposition) continue;
        const disposition = bulkFindings[f.id];
        if (disposition) await post(`/projects/${projectId}/findings/${f.id}/disposition`, { disposition });
      }
      for (const q of understanding?.questions ?? []) {
        if (q.status !== "open") continue;
        const answer = (bulkQuestions[q.id] ?? "").trim();
        if (answer) await post(`/projects/${projectId}/questions/${q.id}/answer`, { answer });
      }
      setConfirmOpen(false);
      flash("success", "Recommendations applied.");
      await reviseInitiative();
    } catch (e) {
      setError(String(e));
      flash("error", `Failed to apply recommendations: ${e}`);
    } finally {
      setBulkBusy(false);
    }
  };

  // Retry a failed initiative by starting a fresh one with the same idea.
  const retryInitiative = async () => {
    if (!initiative) return;
    setBusy(true);
    setError("");
    try {
      const created = await post<{ initiative_id: string }>(`/projects/${projectId}/initiatives`, {
        title: initiative.title,
        description: initiative.description,
      });
      await loadInitiatives(projectId, created.initiative_id); // jump to the fresh attempt
      flash("info", "Retry started — a fresh initiative is now exploring your idea.");
    } catch (e) {
      setError(String(e));
      flash("error", `Retry failed: ${e}`);
    } finally {
      setBusy(false);
    }
  };

  const exportArtifact = async (id: string, kind: string) => {
    try {
      const text = await apiRaw("GET", `/projects/${projectId}/artifacts/${id}/raw`);
      const blob = new Blob([text], { type: "text/markdown" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `${kind}.md`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      setError(String(e));
    }
  };

  const viewArtifact = async (id: string, kind: string) => {
    try {
      const v = await get<{ kind: string; body: string }>(
        `/projects/${projectId}/artifacts/${id}/body`,
      );
      setArtifactView({ kind: kind || v.kind, body: v.body });
    } catch (e) {
      setError(String(e));
    }
  };

  // Change-set actions against the M3 endpoints; each reloads the list on
  // success and fails closed with a toast on error (§6 fail-closed UX).
  const reviewChangeSet = async (id: string, verdict: string, label: string) => {
    try {
      await post(`/projects/${projectId}/change-sets/${id}/review`, { verdict });
      flash("success", `${label} recorded.`);
      await loadChangeSets();
    } catch (e) {
      setError(String(e));
      flash("error", `Change-set ${label.toLowerCase()} failed: ${e}`);
    }
  };

  const applyChangeSet = async (id: string) => {
    try {
      await post(`/projects/${projectId}/change-sets/${id}/apply`, {});
      flash("success", "Change-set applied.");
      await loadChangeSets();
    } catch (e) {
      setError(String(e));
      flash("error", `Apply failed: ${e}`);
    }
  };

  const revertChangeSet = async (id: string) => {
    try {
      await post(`/projects/${projectId}/change-sets/${id}/revert`, {});
      flash("success", "Change-set reverted.");
      await loadChangeSets();
    } catch (e) {
      setError(String(e));
      flash("error", `Revert failed: ${e}`);
    }
  };

  const viewChangeDiff = async (cs: ChangeSet) => {
    try {
      const raw = await apiRaw("GET", `/projects/${projectId}/change-sets/${cs.id}/diff`);
      let body = raw;
      try {
        const parsed = JSON.parse(raw);
        if (parsed && typeof parsed === "object") {
          body = parsed.patch ?? parsed.diff ?? parsed.text ?? parsed.body ?? raw;
        }
      } catch {
        /* plain-text patch — use the raw response */
      }
      setDiffView({ humanId: cs.human_id, body });
    } catch {
      flash("info", `Diff view not available yet (patch ${(cs.patch_digest || "").slice(0, 12)}…).`);
    }
  };

  // executeNextTask runs the next pending task for the initiative, then reloads
  // the change-set list so newly created change sets appear.
  const executeNextTask = async () => {
    if (!initiative) return;
    setExecuting(true);
    setError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiative.id}/execute`, {});
      flash("info", "Execution started — running the next task.");
      await loadChangeSets();
    } catch (e) {
      setError(String(e));
      flash("error", `Execute failed: ${e}`);
    } finally {
      setExecuting(false);
    }
  };

  // verifyRequirement records a passing verification for one requirement code,
  // then refetches the initiative so the stepper can advance to "complete".
  const verifyRequirement = async (code: string) => {
    if (!initiative || verifying) return;
    setVerifying(true);
    setError("");
    try {
      const note = (verifyNotes[code] ?? "").trim();
      const res = await post<VerifyResponse>(
        `/projects/${projectId}/initiatives/${initiative.id}/verify`,
        {
          requirement: code,
          pass: true,
          note: note || undefined,
        },
      );
      setCoverage(res.coverage ?? []);
      setVerifyStatus(res.status ?? "");
      setVerifyNotes((prev) => ({ ...prev, [code]: "" }));
      await loadInitiativeData(projectId, initiative);
      flash(
        "success",
        res.status === "complete" ? `Verified ${code} — initiative complete.` : `Verified ${code}.`,
      );
    } catch (e) {
      setError(String(e));
      flash("error", `Verification failed: ${e}`);
    } finally {
      setVerifying(false);
    }
  };

  // addPasteDoc ingests pasted text as a context doc.
  const addPasteDoc = async () => {
    if (!initiative || !docsPaste.trim()) return;
    setDocsBusy(true);
    setError("");
    try {
      await post<ContextDoc>(`/projects/${projectId}/initiatives/${initiative.id}/docs`, {
        content: docsPaste,
        kind: docsKind,
        title: docsTitle.trim() || undefined,
      });
      setDocsPaste("");
      setDocsTitle("");
      await loadDocs();
      flash("success", "Context doc added.");
    } catch (e) {
      setError(String(e));
      flash("error", `Failed to add doc: ${e}`);
    } finally {
      setDocsBusy(false);
    }
  };

  // addFileDoc uploads a file (md/txt/pdf/docx); the daemon extracts text.
  const addFileDoc = async (file: File) => {
    if (!initiative) return;
    setDocsBusy(true);
    setError("");
    try {
      await uploadDoc<ContextDoc>(`/projects/${projectId}/initiatives/${initiative.id}/docs`, file, {
        kind: docsKind,
        title: docsTitle.trim(),
      });
      setDocsTitle("");
      await loadDocs();
      flash("success", `Uploaded ${file.name}.`);
    } catch (e) {
      setError(String(e));
      flash("error", `Upload failed: ${e}`);
    } finally {
      setDocsBusy(false);
    }
  };

  const commands: Command[] = [
    {
      label: "New idea",
      hint: "create an initiative",
      run: () => {
        setCreating(true);
        setPaletteOpen(false);
      },
    },
    {
      label: "Open Decisions",
      hint: "jump to decisions",
      run: () => {
        document.getElementById("decisions")?.scrollIntoView({ behavior: "smooth" });
        setPaletteOpen(false);
      },
    },
    {
      label: "Show What Changed",
      hint: "jump to activity",
      run: () => {
        document.getElementById("activity")?.scrollIntoView({ behavior: "smooth" });
        setPaletteOpen(false);
      },
    },
    {
      label: "Open Settings",
      hint: "models & providers",
      run: () => {
        setView("settings");
        setPaletteOpen(false);
      },
    },
  ];

  const filtered = commands.filter((c) =>
    c.label.toLowerCase().includes(query.toLowerCase()),
  );

  if (projects.length === 0) {
    return (
      <div className="empty">
        <h1>idea-harness</h1>
        <p className="muted">
          No projects yet. Run <code>idea init</code> in a project directory to get started.
        </p>
      </div>
    );
  }

  const showCreate = creating || initiatives.length === 0;

  if (showCreate) {
    return (
      <div className="empty">
        {initiatives.length > 0 && (
          <button className="ghost" onClick={() => setCreating(false)}>
            ← Back
          </button>
        )}
        <h1>What are you thinking about building?</h1>
        <textarea
          className="idea-input"
          placeholder="Describe the idea, feature, problem, or change..."
          value={idea}
          onChange={(e) => setIdea(e.target.value)}
          rows={4}
          autoFocus
        />
        <div className="row">
          <button onClick={createIdea} disabled={busy || !idea.trim()}>
            {busy ? "Starting…" : "Start"}
          </button>
          <span className="muted">Starts exploration, research, and a reviewed PRD.</span>
        </div>
        {error && <p className="error">{error}</p>}
      </div>
    );
  }

  if (!initiative) return null;

  // Status from the loaded understanding, falling back to the initiative list
  // row so a just-created/retried initiative shows "running" immediately.
  const status = understanding?.status ?? initiative.status;

  const openDecisions = (understanding?.decisions ?? []).filter((d) => d.status === "open");
  const openQuestions = (understanding?.questions ?? []).filter((q) => q.status === "open");
  const openFindings = (findings ?? []).filter((f) => !f.disposition);

  // Phase stepper: each step is done / active / blocked / locked, driven by the
  // artifacts that must exist to advance (§12 workflow DAG).
  const artifactKinds = (artifacts ?? []).map((a) => a.kind);
  const exploreDone = artifactKinds.includes("idea_brief") && artifactKinds.includes("research_brief");
  const defineDone = artifactKinds.includes("prd");
  const designDone = ["technical_design", "design_spec", "verification_plan"].every((k) => artifactKinds.includes(k));
  const planDone = status === "plan_approved" || status === "complete";
  const buildDone = (changeSets ?? []).some((cs) => cs.status === "applied");
  const verifyDone = status === "complete";
  // The executor picks the first ready/proposed task, so gate "Run next task" on
  // the presence of one (otherwise the call would 409 from the daemon).
  const hasExecutableTask = (tasks ?? []).some((t) => t.status === "ready" || t.status === "proposed");
  const dones = [exploreDone, defineDone, designDone, planDone, buildDone, verifyDone];
  const phaseSteps = [
    { label: "Explore", hint: "framing + research" },
    { label: "Define", hint: "a reviewed PRD" },
    { label: "Design", hint: "architecture, design, verification" },
    { label: "Plan", hint: "an approved execution plan" },
    { label: "Build", hint: "applied change sets" },
    { label: "Verify", hint: "verification" },
  ];
  const firstIncomplete = dones.findIndex((d) => !d);
  const stepStates = phaseSteps.map((_, i) => {
    if (dones[i]) return "done";
    if (firstIncomplete === -1 || i > firstIncomplete) return "locked";
    return status === "needs_human_judgment" ? "blocked" : "active";
  });
  const blockedHint =
    status === "needs_human_judgment"
      ? `${openFindings.length} finding(s) · ${openDecisions.length} decision(s) · ${openQuestions.length} question(s) need your judgment`
      : "";

  const answeredCount = Object.values(bulkQuestions).filter((v) => v && v.trim()).length;
  const suggestProgress = Math.round((answeredCount / Math.max(1, openQuestions.length)) * 100);

  return (
    <div className="app">
      <header className="topbar">
        <span className="brand">idea-harness</span>
        <select
          value={projectId}
          onChange={(e) => setProjectId(e.target.value)}
          className="project-picker"
        >
          {projects.map((p) => (
            <option key={p.project_id} value={p.project_id}>
              {p.name}
            </option>
          ))}
        </select>
        {initiatives.length > 1 && (
          <select
            value={initiative.id}
            onChange={(e) => {
              const ini = initiatives.find((i) => i.id === e.target.value);
              if (ini) setInitiative(ini);
            }}
            className="project-picker"
            title="Switch initiative"
          >
            {initiatives.map((i, idx) => (
              <option key={i.id} value={i.id}>
                #{idx + 1} · {i.title.slice(0, 40)}
              </option>
            ))}
          </select>
        )}
        <nav className="nav">
          <button
            className={view === "ideas" ? "nav-item active" : "nav-item"}
            onClick={() => setView("ideas")}
          >
            Ideas
          </button>
          <button
            className={view === "settings" ? "nav-item active" : "nav-item"}
            onClick={() => setView("settings")}
          >
            Settings
          </button>
        </nav>
        <button className="palette-trigger" onClick={() => setPaletteOpen(true)} title="Command palette">
          ⌘K
        </button>
        <button className="new-idea-btn" onClick={() => setCreating(true)}>
          + New idea
        </button>
      </header>

      {demo && (
        <div className="banner">
          Demo mode — synthetic output. Configure a model in{" "}
          <a href="#settings" onClick={() => setView("settings")}>Settings</a>.
        </div>
      )}

      {status === "running" && (
        <div className="banner running-banner">
          <span className="spinner" aria-hidden="true" />
          <span>Working… exploring, researching, and drafting a reviewed PRD.</span>
        </div>
      )}

      {status === "failed" && (
        <div className="banner error-banner">
          <div>
            <strong>Workflow failed.</strong>
            {understanding?.failure_reason && <span> {understanding.failure_reason}</span>}
          </div>
          <div className="row">
            <button onClick={retryInitiative} disabled={busy}>
              {busy ? "Retrying…" : "Retry"}
            </button>
            <button className="ghost small" onClick={() => setTab("logs")}>
              View logs
            </button>
          </div>
        </div>
      )}

      {notice && (
        <div className={`toast ${notice.kind}`} role="status">
          {notice.text}
        </div>
      )}

      {view === "settings" && <Settings projectId={projectId} />}

      {view === "ideas" && (
        <main className="home">
          {error && <p className="error">{error}</p>}

          <nav className="tabs" role="tablist">
            <button className={tab === "overview" ? "tab active" : "tab"} onClick={() => setTab("overview")}>
              Overview
            </button>
            <button className={tab === "requirements" ? "tab active" : "tab"} onClick={() => setTab("requirements")}>
              Requirements
              <span className="tab-count">{(understanding?.requirements ?? []).length}</span>
            </button>
            <button className={tab === "decisions" ? "tab active" : "tab"} onClick={() => setTab("decisions")}>
              Decisions
              <span className="tab-count">{openDecisions.length}</span>
            </button>
            <button className={tab === "findings" ? "tab active" : "tab"} onClick={() => setTab("findings")}>
              Findings
              <span className="tab-count">{openFindings.length}</span>
            </button>
            <button className={tab === "questions" ? "tab active" : "tab"} onClick={() => setTab("questions")}>
              Questions
              <span className="tab-count">{openQuestions.length}</span>
            </button>
            <button className={tab === "artifacts" ? "tab active" : "tab"} onClick={() => setTab("artifacts")}>
              Artifacts
              <span className="tab-count">{(artifacts ?? []).length}</span>
            </button>
            <button className={tab === "tasks" ? "tab active" : "tab"} onClick={() => setTab("tasks")}>
              Tasks
              <span className="tab-count">{(tasks ?? []).length}</span>
            </button>
            <button className={tab === "changes" ? "tab active" : "tab"} onClick={() => setTab("changes")}>
              Changes
              <span className="tab-count">{(changeSets ?? []).length}</span>
            </button>
            <button className={tab === "context" ? "tab active" : "tab"} onClick={() => setTab("context")}>
              Context
              <span className="tab-count">{docs.length}</span>
            </button>
            <button className={tab === "activity" ? "tab active" : "tab"} onClick={() => setTab("activity")}>
              Activity
            </button>
            <button className={tab === "logs" ? "tab active" : "tab"} onClick={() => setTab("logs")}>
              Logs
            </button>
          </nav>

          {tab === "overview" && (
            <>
              {status !== "failed" && (
                <div className="stepper" role="list" aria-label="Progress">
                  {phaseSteps.map((s, i) => (
                    <span
                      key={s.label}
                      role="listitem"
                      className={`step ${stepStates[i]}`}
                      title={s.hint}
                    >
                      {stepStates[i] === "done" && <span className="step-check">✓</span>}
                      {stepStates[i] === "blocked" && <span className="step-check">!</span>}
                      {stepStates[i] === "locked" && <span className="step-check">🔒</span>}
                      {s.label}
                    </span>
                  ))}
                </div>
              )}
              {blockedHint && <p className="muted small-note">{blockedHint}</p>}
              {!blockedHint && firstIncomplete >= 0 && (
                <p className="muted small-note">
                  Next: <strong>{phaseSteps[firstIncomplete].label}</strong> — needs {phaseSteps[firstIncomplete].hint}.
                </p>
              )}
              {!verifyDone && buildDone && (
                <div className="row">
                  <button className="ghost small" onClick={() => setTab("requirements")}>
                    Verify requirements
                  </button>
                  <span className="muted small-note">
                    record a pass per requirement to complete the initiative (or run{" "}
                    <code>idea verify REQ-1 --pass</code>).
                  </span>
                </div>
              )}

              <section className="card">
                <h2>Current Understanding</h2>
                <p className="lead">
                  {status === "approved" && <span className="badge ok">approved</span>}
                  {status === "plan_approved" && <span className="badge ok">plan approved</span>}
                  {status === "needs_human_judgment" && <span className="badge warn">needs your judgment</span>}
                  {status === "running" && <span className="badge warn">running</span>}
                  {status === "failed" && <span className="badge warn">failed</span>}
                </p>
                {understanding?.summary && <p className="summary">{understanding.summary}</p>}
              </section>

              {nextMove && (
                <section className="card accent">
                  <h2>Recommended Next Move</h2>
                  <p className="lead">{nextMove.action}</p>
                  <p className="muted">{nextMove.why}</p>
                </section>
              )}

              {status === "approved" && (
                <section className="card accent">
                  <h2>Ready to Design &amp; Plan</h2>
                  <p className="muted">
                    The PRD is reviewed. Proceed to architecture, product design, verification, and an execution plan.
                  </p>
                  <div className="row">
                    <button onClick={advanceInitiative} disabled={advancing}>
                      {advancing ? "Advancing…" : "Proceed to design & plan"}
                    </button>
                  </div>
                </section>
              )}

              {(tasks ?? []).length > 0 && (
                <section className="card">
                  <div className="card-head">
                    <h2>Execution Plan</h2>
                    <button className="ghost small" onClick={executeNextTask} disabled={executing || !hasExecutableTask}>
                      {executing ? "Running…" : "Run next task"}
                    </button>
                  </div>
                  <ul className="list">
                    {(tasks ?? []).map((t) => (
                      <li key={t.id}>
                        <span className={`tag ${t.priority}`}>{t.priority}</span>
                        <span className="code">{t.code}</span> {t.title}
                        <button className="ghost small" onClick={() => openTrace("task", t.id, t.code)}>
                          Why?
                        </button>
                      </li>
                    ))}
                  </ul>
                </section>
              )}

              {status === "needs_human_judgment" && (
                <section className="card judgment">
                  <h2>Needs Your Judgment</h2>
                  <p className="muted">
                    {openDecisions.length} decision{openDecisions.length === 1 ? "" : "s"} ·{" "}
                    {openQuestions.length} question{openQuestions.length === 1 ? "" : "s"} ·{" "}
                    {openFindings.length} finding{openFindings.length === 1 ? "" : "s"}
                    {openDecisions.length === 0 && openQuestions.length === 0 && openFindings.length === 0 && (
                      <> — everything is resolved; apply your feedback to revise the PRD.</>
                    )}
                  </p>
                  {(openDecisions.length > 0 || openQuestions.length > 0 || openFindings.length > 0) && (
                    <div className="row">
                      <button onClick={openTakeRecommendations}>
                        Take recommendations &amp; revise
                      </button>
                      {openDecisions.length > 0 && <button className="ghost" onClick={() => setTab("decisions")}>Review decisions</button>}
                      {openFindings.length > 0 && <button className="ghost" onClick={() => setTab("findings")}>Review findings</button>}
                      {openQuestions.length > 0 && <button className="ghost" onClick={() => setTab("questions")}>Answer questions</button>}
                    </div>
                  )}
                  <div className="row">
                    <button className="ghost" onClick={reviseInitiative} disabled={revising}>
                      {revising ? "Revising…" : "Apply my decisions & revise PRD"}
                    </button>
                  </div>
                </section>
              )}

              <section className="card">
                <h2>Key facts &amp; assumptions</h2>
                {(understanding?.assertions ?? []).length === 0 ? (
                  <p className="muted">None yet.</p>
                ) : (
                  <ul className="list">
                    <CollapsibleList
                      items={understanding?.assertions ?? []}
                      limit={4}
                      render={(a) => (
                        <li key={a.id}>
                          <span className={`tag ${a.type}`}>{a.type}</span> {a.statement}
                          <span className="muted"> ({a.confidence})</span>
                        </li>
                      )}
                    />
                  </ul>
                )}
              </section>
            </>
          )}

          {tab === "requirements" && (
            <section className="card">
              <div className="card-head">
                <h2>Requirements</h2>
                {verifyStatus === "complete" && <span className="badge ok">✓ complete</span>}
              </div>
              {coverage.length > 0 && (
                <p className="muted small-note">
                  verification coverage:{" "}
                  {coverage.map((c) => `${c.code} ${c.pass}/${c.total}`).join(" · ")}
                </p>
              )}
              {(understanding?.requirements ?? []).length === 0 ? (
                <p className="muted">None yet.</p>
              ) : (
                <ul className="list">
                  {(understanding?.requirements ?? []).map((r) => {
                    const cov = coverage.find((c) => c.code === r.code);
                    return (
                      <li key={r.id}>
                        <span className="code">{r.code}</span> {r.statement}
                        <ul className="sub">
                          {(r.acceptance_criteria ?? []).map((ac, i) => (
                            <li key={i} className="muted">
                              {ac.metric} {ac.target} ({ac.condition})
                            </li>
                          ))}
                        </ul>
                        {cov && (
                          <p className="muted small-note">
                            coverage: {cov.pass}/{cov.total}
                            {cov.needs_manual > 0 && <span className="badge warn">needs manual</span>}
                          </p>
                        )}
                        <div className="row inline-note">
                          <input
                            className="text-input"
                            placeholder="Note (optional)…"
                            value={verifyNotes[r.code] ?? ""}
                            onChange={(e) =>
                              setVerifyNotes((prev) => ({ ...prev, [r.code]: e.target.value }))
                            }
                          />
                          <button
                            className="ghost small"
                            onClick={() => verifyRequirement(r.code)}
                            disabled={verifying}
                          >
                            {verifying ? "Verifying…" : "Pass"}
                          </button>
                        </div>
                      </li>
                    );
                  })}
                </ul>
              )}
            </section>
          )}

          {tab === "decisions" && (
            <section className="card">
              <div className="card-head">
                <h2>Decisions</h2>
                {status === "needs_human_judgment" && openDecisions.length > 0 && (
                  <button className="ghost small" onClick={openTakeRecommendations}>Take recommendations</button>
                )}
              </div>
              {(understanding?.decisions ?? []).length === 0 ? (
                <p className="muted">No decisions needed.</p>
              ) : (
                <ul className="list">
                  <CollapsibleList
                    items={understanding?.decisions ?? []}
                    limit={5}
                    render={(d) => (
                      <li key={d.id}>
                        <span className="code">{d.code}</span> {d.question}
                        {d.dissents && d.dissents.length > 0 && (
                          <div className="dissents">
                            {(d.dissents ?? []).map((x, i) => (
                              <div key={i} className="muted">
                                <span className="tag hypothesis">{x.proponent_role}</span> {x.position}
                              </div>
                            ))}
                          </div>
                        )}
                        {d.status === "open" ? (
                          <>
                            <p className="muted">Recommendation: {d.recommendation || "(none)"}</p>
                            <div className="options">
                              <button className="ghost small" onClick={() => resolveDecision(d.id, d.recommendation)}>
                                Accept
                              </button>
                              <button className="ghost small" onClick={() => resolveDecision(d.id, "declined")}>
                                Decline
                              </button>
                              {editingDecision === d.id ? (
                                <span className="row inline-edit">
                                  <input
                                    className="text-input"
                                    placeholder="Custom resolution…"
                                    value={decisionDrafts[d.id] ?? ""}
                                    onChange={(e) =>
                                      setDecisionDrafts((prev) => ({ ...prev, [d.id]: e.target.value }))
                                    }
                                    autoFocus
                                  />
                                  <button className="ghost small" onClick={() => saveDecisionEdit(d.id)}>
                                    Save
                                  </button>
                                  <button className="ghost small" onClick={() => setEditingDecision(null)}>
                                    Cancel
                                  </button>
                                </span>
                              ) : (
                                <button className="ghost small" onClick={() => setEditingDecision(d.id)}>
                                  Edit…
                                </button>
                              )}
                            </div>
                            <div className="options">
                              {(d.options ?? [])
                                .filter((o) => o !== d.recommendation)
                                .map((o) => (
                                  <button key={o} className="ghost small" onClick={() => resolveDecision(d.id, o)}>
                                    {o}
                                  </button>
                                ))}
                            </div>
                            <div className="row">
                              <button className="ghost small" onClick={() => toggleDiscuss(d.id)}>
                                {discussingDecision === d.id ? "Close discussion" : "Discuss…"}
                              </button>
                            </div>
                            {discussingDecision === d.id && (
                              <div className="discussion">
                                {(discussionThreads[d.id] ?? []).length === 0 ? (
                                  <p className="muted">Ask for more context, or propose your own solution.</p>
                                ) : (
                                  <ul className="list">
                                    {(discussionThreads[d.id] ?? []).map((m) => (
                                      <li key={m.id} className={m.author === "human" ? "disc-human" : "disc-ai"}>
                                        <span className="code">{m.author === "human" ? "you" : "pm"}</span>
                                        <span>{m.message}</span>
                                      </li>
                                    ))}
                                  </ul>
                                )}
                                <div className="row inline-note">
                                  <input
                                    className="text-input"
                                    placeholder="Ask for context, or propose your own solution…"
                                    value={discussionDrafts[d.id] ?? ""}
                                    onChange={(e) => setDiscussionDrafts((prev) => ({ ...prev, [d.id]: e.target.value }))}
                                  />
                                  <button className="ghost small" onClick={() => sendDecisionDiscussion(d.id)} disabled={discussing}>
                                    {discussing ? "Sending…" : "Send"}
                                  </button>
                                </div>
                              </div>
                            )}
                          </>
                        ) : d.resolution === "declined" ? (
                          <span className="badge warn">✗ declined</span>
                        ) : (
                          <>
                            <span className="badge ok">✓ {d.resolution === d.recommendation ? "accepted" : "decided"}</span>
                            <span className="muted"> {d.resolution || d.recommendation}</span>
                          </>
                        )}
                      </li>
                    )}
                  />
                </ul>
              )}
              <p className="muted small-note">
                Decisions are immutable once made — to change one, revise the PRD and a new decision is raised. Decisions take precedence over open questions.
              </p>
            </section>
          )}

          {tab === "findings" && (
            <section className="card">
              <div className="card-head">
                <h2>Review Findings</h2>
                {status === "needs_human_judgment" && openFindings.length > 0 && (
                  <button className="ghost small" onClick={openTakeRecommendations}>Take recommendations</button>
                )}
              </div>
              <p className="muted small-note">
                <strong>Fix it</strong> = incorporate the mitigation into the PRD on the next revision.{" "}
                <strong>Accept risk</strong> = "I understand this risk and accept it as-is" (clears the blocker).{" "}
                <strong>Not applicable</strong> = it doesn't apply to your product.{" "}
                <strong>Defer</strong> = postpone it (still blocks approval). Add a note below to discuss a finding with the team.
              </p>
              {(findings ?? []).length === 0 ? (
                <p className="muted">No findings.</p>
              ) : (
                <ul className="list">
                  <CollapsibleList
                    items={findings ?? []}
                    limit={5}
                    render={(f) => (
                      <li key={f.id}>
                        <span className={`tag ${f.severity}`}>{f.severity}</span> {f.finding}
                        {f.impact && <p className="muted">Impact: {f.impact}</p>}
                        {f.recommendation && <p className="muted">Recommendation: {f.recommendation}</p>}
                        {f.note && <p className="note-text">💬 {f.note}</p>}
                        {f.disposition ? (
                          <>
                            {" · "}
                            <span className={`tag ${dispositionClass(f.disposition)}`}>{dispositionLabel(f.disposition)}</span>
                          </>
                        ) : (
                          <div className="options">
                            <button className="ghost small" onClick={() => dispositionFinding(f.id, "mitigate")}>Fix it</button>
                            <button className="ghost small" onClick={() => dispositionFinding(f.id, "accepted_risk")}>Accept risk</button>
                            <button className="ghost small" onClick={() => dispositionFinding(f.id, "not_applicable")}>Not applicable</button>
                            <button className="ghost small" onClick={() => dispositionFinding(f.id, "deferred")}>Defer</button>
                          </div>
                        )}
                        <div className="row inline-note">
                          <input
                            className="text-input"
                            placeholder="Discuss — add a note…"
                            value={findingNotes[f.id] ?? ""}
                            onChange={(e) => setFindingNotes((prev) => ({ ...prev, [f.id]: e.target.value }))}
                          />
                          <button className="ghost small" onClick={() => saveFindingNote(f.id)}>
                            Add note
                          </button>
                        </div>
                      </li>
                    )}
                  />
                </ul>
              )}
            </section>
          )}

          {tab === "questions" && (
            <section className="card">
              <div className="card-head">
                <h2>Open Questions</h2>
                {status === "needs_human_judgment" && openQuestions.length > 0 && (
                  <button className="ghost small" onClick={openTakeRecommendations}>Take recommendations</button>
                )}
              </div>
              {openQuestions.length === 0 ? (
                <p className="muted">None.</p>
              ) : (
                <ul className="list">
                  <CollapsibleList
                    items={openQuestions}
                    limit={5}
                    render={(q) => (
                      <li key={q.id}>
                        {q.question}
                        <div className="row">
                          <input
                            className="text-input"
                            placeholder="Your answer…"
                            value={answerDrafts[q.id] ?? ""}
                            onChange={(e) => setAnswerDrafts((prev) => ({ ...prev, [q.id]: e.target.value }))}
                          />
                          <button className="ghost" onClick={() => answerQuestion(q.id)}>Answer</button>
                        </div>
                      </li>
                    )}
                  />
                </ul>
              )}
            </section>
          )}

          {tab === "artifacts" && (
            <section className="card">
              <h2>Artifacts</h2>
              {(artifacts ?? []).length === 0 ? (
                <p className="muted">None yet.</p>
              ) : (
                <ul className="list">
                  {(artifacts ?? []).map((a) => (
                    <li key={a.id} className="artifact-row">
                      <span className="code">{a.kind}</span>
                      <span className="muted">v{a.version}</span>
                      <button className="ghost small" onClick={() => viewArtifact(a.id, a.kind)}>View</button>
                      <button className="ghost small" onClick={() => exportArtifact(a.id, a.kind)}>Export</button>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          )}

          {tab === "tasks" && (
            <section className="card">
              <h2>Execution Plan Tasks</h2>
              {(tasks ?? []).length === 0 ? (
                <p className="muted">No tasks yet — proceed to design &amp; plan.</p>
              ) : (
                <ul className="list">
                  {(tasks ?? []).map((t) => (
                    <li key={t.id}>
                      <span className={`tag ${t.priority}`}>{t.priority}</span>
                      <span className="code">{t.code}</span> {t.title}
                      {t.estimate && <span className="muted"> · {t.estimate}</span>}
                      <p className="muted">{t.statement}</p>
                      <button className="ghost small" onClick={() => openTrace("task", t.id, t.code)}>
                        Why?
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          )}

          {tab === "changes" && (
            <section className="card">
              <div className="card-head">
                <h2>Change Sets</h2>
                <button className="ghost small" onClick={executeNextTask} disabled={executing || !hasExecutableTask}>
                  {executing ? "Running…" : "Run next task"}
                </button>
              </div>
              {(changeSets ?? []).length === 0 ? (
                <p className="muted">No change sets yet.</p>
              ) : (
                <ul className="changes-list">
                  {(changeSets ?? []).map((cs) => (
                    <li key={cs.id} className="change-card">
                      <div className="change-head">
                        <span className={`change-status ${changeStatusClass(cs.status)}`}>
                          {dispositionLabel(cs.status)}
                        </span>
                        <span className="code">{cs.human_id}</span>
                        {cs.patch_digest && (
                          <span className="muted" title={cs.patch_digest}>
                            {cs.patch_digest.slice(0, 12)}…
                          </span>
                        )}
                      </div>
                      {cs.git_commit_sha && (
                        <div className="muted" title={cs.git_commit_sha}>
                          commit: {cs.git_commit_sha.slice(0, 8)}
                        </div>
                      )}
                      <div className="change-actions">
                        <button className="ghost small" onClick={() => viewChangeDiff(cs)}>View diff</button>
                        {cs.status === "review_required" && (
                          <>
                            <button className="ghost small" onClick={() => reviewChangeSet(cs.id, "approved", "Approve")}>Approve</button>
                            <button className="ghost small" onClick={() => reviewChangeSet(cs.id, "changes_requested", "Request changes")}>Request changes</button>
                            <button className="ghost small" onClick={() => reviewChangeSet(cs.id, "rejected", "Reject")}>Reject</button>
                          </>
                        )}
                        {cs.status === "checks_running" && (
                          <button className="ghost small" onClick={() => reviewChangeSet(cs.id, "rejected", "Reject")}>Reject</button>
                        )}
                        {cs.status === "approved" && (
                          <button className="ghost small" onClick={() => applyChangeSet(cs.id)}>Apply</button>
                        )}
                        {cs.status === "applied" && (
                          <button className="ghost small" onClick={() => revertChangeSet(cs.id)}>Revert</button>
                        )}
                      </div>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          )}

          {tab === "context" && (
            <section className="card">
              <div className="card-head">
                <h2>Context Docs</h2>
                <span className="muted small-note">Upload a PRD, design, tech spec, or market analysis to ground the agents.</span>
              </div>
              {docs.length === 0 ? (
                <p className="muted">No docs yet — add one below.</p>
              ) : (
                <ul className="list">
                  {docs.map((d) => (
                    <li key={d.id}>
                      <span className="code">{d.kind}</span> <strong>{d.title}</strong>
                      {d.format === "image" && (
                        <span className="tag fact">image</span>
                      )}
                      {d.format === "image" && !d.description_digest && (
                        <span className="muted"> describing…</span>
                      )}
                      {d.preview && <p className="muted">{d.preview}</p>}
                      <button className="ghost small" onClick={() => openTrace("context_doc", d.id, d.title)}>
                        Trace
                      </button>
                    </li>
                  ))}
                </ul>
              )}

              <div className="row" style={{ marginTop: 12, gap: 8 }}>
                <select className="text-input" value={docsKind} onChange={(e) => setDocsKind(e.target.value)}>
                  {["prd", "design", "tech", "market", "other"].map((k) => (
                    <option key={k} value={k}>{k}</option>
                  ))}
                </select>
                <input
                  className="text-input"
                  placeholder="Title (optional)"
                  value={docsTitle}
                  onChange={(e) => setDocsTitle(e.target.value)}
                />
              </div>

              <textarea
                className="text-input"
                style={{ marginTop: 8, width: "100%", minHeight: 120 }}
                placeholder="…or paste text (Markdown / plain text) here."
                value={docsPaste}
                onChange={(e) => setDocsPaste(e.target.value)}
              />
              <div className="row" style={{ marginTop: 8, gap: 8 }}>
                <button onClick={addPasteDoc} disabled={docsBusy || !docsPaste.trim()}>
                  {docsBusy ? "Adding…" : "Add pasted text"}
                </button>
                <label className="ghost small" style={{ cursor: "pointer" }}>
                  Upload file (.md · .txt · .pdf · .docx · .xlsx · image)
                  <input
                    type="file"
                    accept=".md,.txt,.markdown,.pdf,.docx,.xlsx,.jpg,.jpeg,.png,.gif,.webp"
                    style={{ display: "none" }}
                    disabled={docsBusy}
                    onChange={(e) => {
                      const f = e.target.files?.[0];
                      if (f) addFileDoc(f);
                      e.target.value = "";
                    }}
                  />
                </label>
              </div>
            </section>
          )}

          {tab === "activity" && (
            <section className="card">
              <h2>What Changed</h2>
              {(changed?.groups ?? []).length === 0 ? (
                <p className="muted">No changes yet.</p>
              ) : (
                (changed?.groups ?? []).map((g) => (
                  <div key={g.category}>
                    <h3>{g.category}</h3>
                    <ul className="list">
                      {(g.items ?? []).map((it, i) => (
                        <li key={i}>{it}</li>
                      ))}
                    </ul>
                  </div>
                ))
              )}
              <h3>Recent events</h3>
              {liveLog.length === 0 ? (
                <p className="muted">Waiting for events…</p>
              ) : (
                <ul className="list live-log">
                  {liveLog.slice(-40).map((l) => (
                    <li key={l.seq} className="muted">
                      {l.text}
                    </li>
                  ))}
                </ul>
              )}
            </section>
          )}

          {tab === "logs" && (
            <section className="card">
              <h2>Daemon logs</h2>
              {logs.length === 0 ? (
                <p className="muted">No log output yet.</p>
              ) : (
                <pre className="log-body">
                  {logs.map((l, i) => (
                    <div key={i}>{l}</div>
                  ))}
                </pre>
              )}
            </section>
          )}
        </main>
      )}

      {paletteOpen && (
        <div className="palette-backdrop" onClick={() => setPaletteOpen(false)}>
          <div className="palette" onClick={(e) => e.stopPropagation()}>
            <input
              className="palette-input"
              placeholder="Type a command…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              autoFocus
            />
            <ul className="palette-list">
              {filtered.map((c) => (
                <li key={c.label}>
                  <button onClick={c.run}>
                    <span>{c.label}</span>
                    <span className="muted">{c.hint}</span>
                  </button>
                </li>
              ))}
            </ul>
          </div>
        </div>
      )}

      {artifactView && (
        <div className="palette-backdrop" onClick={() => setArtifactView(null)}>
          <div className="artifact-modal" onClick={(e) => e.stopPropagation()}>
            <div className="artifact-modal-head">
              <span className="code">{artifactView.kind}</span>
              <button className="ghost small" onClick={() => setArtifactView(null)}>
                Close
              </button>
            </div>
            <pre className="artifact-body">{artifactView.body}</pre>
          </div>
        </div>
      )}

      {diffView && (
        <div className="palette-backdrop" onClick={() => setDiffView(null)}>
          <div className="artifact-modal" onClick={(e) => e.stopPropagation()}>
            <div className="artifact-modal-head">
              <span className="code">diff — {diffView.humanId}</span>
              <button className="ghost small" onClick={() => setDiffView(null)}>
                Close
              </button>
            </div>
            <pre className="diff-body">{diffView.body}</pre>
          </div>
        </div>
      )}

      {confirmOpen && (
        <div className="palette-backdrop" onClick={() => setConfirmOpen(false)}>
          <div className="confirm-modal" onClick={(e) => e.stopPropagation()}>
            <div className="artifact-modal-head">
              <span className="code">Confirm &amp; revise</span>
              <button className="ghost small" onClick={() => setConfirmOpen(false)}>
                Close
              </button>
            </div>
            <div className="confirm-body">
              <p className="muted small-note">
                Review the recommended answers below. Edit any item before applying, then confirm to apply them and revise the PRD.
              </p>

              <details open>
                <summary>Decisions · {openDecisions.length}</summary>
                {openDecisions.length === 0 ? (
                  <p className="muted">No open decisions.</p>
                ) : (
                  <ul className="list">
                    {openDecisions.map((d) => (
                      <li key={d.id} className="confirm-item">
                        <span className="muted">{d.question}</span>
                        <select
                          className="text-input"
                          value={bulkDecisions[d.id] ?? d.recommendation}
                          onChange={(e) => setBulkDecisions((prev) => ({ ...prev, [d.id]: e.target.value }))}
                        >
                          {(d.options ?? []).map((o) => (
                            <option key={o} value={o}>{o}</option>
                          ))}
                          <option value="declined">Decline</option>
                        </select>
                      </li>
                    ))}
                  </ul>
                )}
              </details>

              <details open>
                <summary>Findings · {openFindings.length}</summary>
                {openFindings.length === 0 ? (
                  <p className="muted">No open findings.</p>
                ) : (
                  <ul className="list">
                    {openFindings.map((f) => (
                      <li key={f.id} className="confirm-item">
                        <span className={`tag ${f.severity}`}>{f.severity}</span>
                        <span className="muted">{f.finding}</span>
                        <select
                          className="text-input"
                          value={bulkFindings[f.id] ?? "mitigate"}
                          onChange={(e) => setBulkFindings((prev) => ({ ...prev, [f.id]: e.target.value }))}
                        >
                          <option value="mitigate">Fix it</option>
                          <option value="accepted_risk">Accept risk</option>
                          <option value="not_applicable">Not applicable</option>
                          <option value="deferred">Defer</option>
                        </select>
                      </li>
                    ))}
                  </ul>
                )}
              </details>

              <details open>
                <summary>Questions · {openQuestions.length}</summary>
                {suggestingAnswers && (
                  <div className="progress-wrap">
                    <div className="progress-track"><div className="progress-bar" style={{ width: `${suggestProgress}%` }} /></div>
                    <p className="muted">Generating suggested answers… {answeredCount}/{openQuestions.length}</p>
                  </div>
                )}
                {openQuestions.length === 0 ? (
                  <p className="muted">No open questions.</p>
                ) : (
                  <ul className="list">
                    {openQuestions.map((q) => (
                      <li key={q.id} className="confirm-item">
                        <span className="muted">{q.question}</span>
                        <input
                          className="text-input"
                          placeholder="Answer (blank to skip)…"
                          value={bulkQuestions[q.id] ?? ""}
                          onChange={(e) => setBulkQuestions((prev) => ({ ...prev, [q.id]: e.target.value }))}
                        />
                      </li>
                    ))}
                  </ul>
                )}
              </details>
            </div>
            <div className="confirm-footer">
              <button className="ghost" onClick={() => setConfirmOpen(false)}>Cancel</button>
              <button onClick={submitRecommendations} disabled={bulkBusy || suggestingAnswers}>
                {bulkBusy ? "Applying…" : suggestingAnswers ? "Generating answers…" : "Confirm & revise PRD"}
              </button>
            </div>
          </div>
        </div>
      )}

      {traceModal && (
        <div className="palette-backdrop" onClick={() => setTraceModal(null)}>
          <div className="confirm-modal" onClick={(e) => e.stopPropagation()}>
            <div className="artifact-modal-head">
              <span className="code">Why &amp; Impact — {traceModal.label}</span>
              <button className="ghost small" onClick={() => setTraceModal(null)}>Close</button>
            </div>
            <div className="confirm-body">
              <details open>
                <summary>Why (provenance)</summary>
                {traceWhy.length === 0 ? (
                  <p className="muted">No provenance edges found.</p>
                ) : (
                  <ul className="list">
                    {traceWhy.map((w, i) => (
                      <li key={i} className="muted">
                        {w.from_label} —{w.edge_type}→ {w.to_label}
                      </li>
                    ))}
                  </ul>
                )}
              </details>
              <details open>
                <summary>Impact (dependents)</summary>
                {traceImpact.length === 0 ? (
                  <p className="muted">No dependents found.</p>
                ) : (
                  <ul className="list">
                    {traceImpact.map((im, i) => (
                      <li key={i} className="muted">
                        <span className="tag mitigate">{im.staleness}</span> {im.from_label} —{im.edge_type}→ {im.to_label}
                      </li>
                    ))}
                  </ul>
                )}
              </details>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
