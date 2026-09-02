import { useCallback, useEffect, useState } from "react";
import { get, post } from "./api";
import Settings from "./Settings";
import PhaseNav from "./PhaseNav";
import PhasePage from "./PhasePage";
import DefinePage from "./DefinePage";
import ExplorePage from "./ExplorePage";
import BuildPage from "./BuildPage";
import PlanPage from "./PlanPage";
import ProductHome from "./ProductHome";
import type { StatusPill } from "./PhaseHeader";
import type { DocumentRowProps } from "./DocumentRow";
import {
  ARTIFACT_PHASE,
  ARTIFACT_TITLES,
  PHASE_LABELS,
  PHASE_PURPOSE,
  currentPhase,
  derivePhases,
  type PhaseSource,
  type PhaseState,
} from "./phases";
import { currentRoute, navigate, type Route, type PhaseName } from "./router";

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
interface Artifact {
  id: string;
  kind: string;
  version: number;
}
interface Task {
  id: string;
  code: string;
  title: string;
  status: string;
}
interface ChangeSet {
  id: string;
  human_id: string;
  status: string;
}

export default function App() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [projectId, setProjectId] = useState("");
  const [initiatives, setInitiatives] = useState<Initiative[]>([]);
  const [route, setRoute] = useState<Route>(() => currentRoute());
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [changeSets, setChangeSets] = useState<ChangeSet[]>([]);
  const [defaultModel, setDefaultModel] = useState("");

  const loadProjects = useCallback(async () => {
    try {
      const ps = (await get<Project[]>("/projects")) ?? [];
      setProjects(ps);
      if (ps.length > 0) setProjectId((prev) => prev || ps[0].project_id);
    } catch {
      /* daemon down — empty state handles it */
    }
  }, []);

  const loadInitiatives = useCallback(async (pid: string) => {
    try {
      const list = (await get<Initiative[]>(`/projects/${pid}/initiatives`)) ?? [];
      setInitiatives(list);
    } catch {
      /* ignore */
    }
  }, []);

  const loadInitiativeData = useCallback(async (pid: string, iniId: string) => {
    const [a, t, c] = await Promise.all([
      get<Artifact[]>(`/projects/${pid}/artifacts?initiative_id=${encodeURIComponent(iniId)}`).catch(() => []),
      get<Task[]>(`/projects/${pid}/initiatives/${iniId}/tasks`).catch(() => []),
      get<ChangeSet[]>(`/projects/${pid}/change-sets?initiative_id=${encodeURIComponent(iniId)}`).catch(() => []),
    ]);
    setArtifacts(a ?? []);
    setTasks(t ?? []);
    setChangeSets(c ?? []);
  }, []);

  useEffect(() => {
    loadProjects();
  }, [loadProjects]);

  useEffect(() => {
    if (!projectId) return;
    loadInitiatives(projectId);
    get<{ default_model: string }>(`/projects/${projectId}/providers`)
      .then((v) => setDefaultModel(v.default_model || ""))
      .catch(() => {});
  }, [projectId, loadInitiatives]);

  // Hash routing.
  useEffect(() => {
    const onHash = () => setRoute(currentRoute());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  const initiativeId =
    route.name !== "home" && route.name !== "settings" ? route.id : "";
  const currentInitiative = initiativeId
    ? initiatives.find((i) => i.id === initiativeId) ?? null
    : null;
  const currentInitiativeId = currentInitiative?.id ?? "";

  useEffect(() => {
    if (projectId && currentInitiativeId) {
      loadInitiativeData(projectId, currentInitiativeId);
    }
  }, [projectId, currentInitiativeId, loadInitiativeData]);

  // Poll for freshness (SSE live-update arrives with the running-state work).
  useEffect(() => {
    if (!projectId) return;
    const id = setInterval(() => {
      loadInitiatives(projectId);
      if (currentInitiativeId) loadInitiativeData(projectId, currentInitiativeId);
    }, 2500);
    return () => clearInterval(id);
  }, [projectId, currentInitiativeId, loadInitiatives, loadInitiativeData]);

  const phaseSource: PhaseSource = {
    status: currentInitiative?.status ?? "",
    artifacts,
    tasks,
    changeSets,
    verifiedRequirements: 0, // wired in the Verify page (step 9)
  };
  const phases = derivePhases(phaseSource);
  const activePhase: PhaseName | "settings" =
    route.name === "phase"
      ? route.phase
      : route.name === "settings"
        ? "settings"
        : currentPhase(phaseSource); // decision/health/doc sub-surfaces

  const createIdea = async (text: string) => {
    if (!projectId || !text.trim()) return;
    try {
      const created = await post<{ initiative_id: string }>(`/projects/${projectId}/initiatives`, {
        title: text.trim(),
        description: text.trim(),
      });
      await loadInitiatives(projectId);
      navigate(`#/i/${created.initiative_id}/explore`);
    } catch {
      /* surfaced below */
    }
  };

  // ---------- render ----------

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

  const project = projects.find((p) => p.project_id === projectId);
  const initiativeIndex = currentInitiative
    ? initiatives.findIndex((i) => i.id === currentInitiative.id) + 1
    : 1;

  return (
    <div className="app-shell">
      <PhaseNav
        initiative={currentInitiative}
        projectName={project?.name ?? ""}
        initiativeIndex={initiativeIndex}
        initiativeCount={initiatives.length}
        phases={phases}
        active={activePhase}
        inbox={[]}
        modelChip={defaultModel ? defaultModel.split("/").pop() || defaultModel : "demo"}
        onNavigate={navigate}
      />

      <main className="phase-main">
        {route.name === "home" && (
          <ProductHome
            projectId={projectId}
            initiatives={initiatives}
            onOpen={(id) => navigate(`#/i/${id}/explore`)}
            onCreate={createIdea}
          />
        )}

        {route.name === "settings" && <Settings projectId={projectId} />}

        {route.name === "phase" && currentInitiative && route.phase === "define" && (
          <DefinePage
            projectId={projectId}
            initiativeId={currentInitiative.id}
            status={currentInitiative.status}
          />
        )}
        {route.name === "phase" && currentInitiative && route.phase === "explore" && (
          <ExplorePage
            projectId={projectId}
            initiativeId={currentInitiative.id}
            status={currentInitiative.status}
          />
        )}
        {route.name === "phase" && currentInitiative && route.phase === "build" && (
          <BuildPage projectId={projectId} initiativeId={currentInitiative.id} />
        )}
        {route.name === "phase" && currentInitiative && route.phase === "plan" && (
          <PlanPage
            projectId={projectId}
            initiativeId={currentInitiative.id}
            status={currentInitiative.status}
          />
        )}
        {route.name === "phase" && currentInitiative && route.phase !== "define" && route.phase !== "explore" && route.phase !== "build" && route.phase !== "plan" && (
          <PhasePage
            title={PHASE_LABELS[route.phase]}
            purpose={PHASE_PURPOSE[route.phase]}
            pill={phasePill(phases.find((p) => p.phase === route.phase), currentInitiative.status, route.phase)}
            docs={artifactRows(artifacts, currentInitiative.id, route.phase)}
          >
            <div className="phase-stub muted">Phase content lands in a later step.</div>
          </PhasePage>
        )}

        {route.name === "decision" && (
          <div className="phase-stub">Decision discussion — step 10</div>
        )}
        {route.name === "health" && (
          <div className="phase-stub">Graph health — step 10</div>
        )}
        {route.name === "doc" && (
          <div className="phase-stub">Document reader — step 10</div>
        )}
      </main>
    </div>
  );
}

function phasePill(state: PhaseState | undefined, status: string, phase: PhaseName): StatusPill | undefined {
  if (!state) return undefined;
  if (state.state === "done") return { label: "approved", state: "verified" };
  if (state.state === "future") return { label: "not started", state: "inactive" };
  switch (status) {
    case "running":
      return { label: "running", state: "running" };
    case "needs_human_judgment":
      return { label: phase === "plan" ? "needs approval" : "waiting on you", state: "needs-you" };
    case "complete":
      return { label: "verified", state: "verified" };
    case "failed":
      return { label: "failing", state: "failing" };
    default:
      return undefined;
  }
}

function artifactRows(artifacts: Artifact[], initiativeId: string, phase: PhaseName): DocumentRowProps[] {
  return artifacts
    .filter((a) => ARTIFACT_PHASE[a.kind] === phase)
    .map((a) => ({
      title: `${ARTIFACT_TITLES[a.kind] ?? a.kind} · v${a.version}`,
      meta: "",
      onRead: () => navigate(`#/i/${initiativeId}/doc/${a.id}/${a.version}`),
    }));
}
