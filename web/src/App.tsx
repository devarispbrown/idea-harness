import { useCallback, useEffect, useState } from "react";
import { get, post } from "./api";
import Settings from "./Settings";
import PhaseNav from "./PhaseNav";
import { currentPhase, derivePhases, type PhaseSource } from "./phases";
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
  const [idea, setIdea] = useState("");

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

  const createIdea = async () => {
    if (!projectId || !idea.trim()) return;
    try {
      const created = await post<{ initiative_id: string }>(`/projects/${projectId}/initiatives`, {
        title: idea.trim(),
        description: idea.trim(),
      });
      setIdea("");
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
            initiatives={initiatives}
            idea={idea}
            setIdea={setIdea}
            createIdea={createIdea}
            onOpen={(id) => navigate(`#/i/${id}/explore`)}
          />
        )}

        {route.name === "settings" && <Settings projectId={projectId} />}

        {route.name === "phase" && (
          <PhaseStub phase={route.phase} initiative={currentInitiative} />
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

function PhaseStub({ phase, initiative }: { phase: PhaseName; initiative: Initiative | null }) {
  const labels: Record<PhaseName, string> = {
    explore: "Explore",
    define: "Define",
    design: "Design",
    plan: "Plan",
    build: "Build",
    verify: "Verify",
  };
  return (
    <div className="phase-stub">
      <h1 style={{ margin: 0 }}>{labels[phase]}</h1>
      <p className="muted">{initiative?.title ?? "No initiative"}</p>
      <p className="muted">Phase content lands in a later step.</p>
    </div>
  );
}

function ProductHome({
  initiatives,
  idea,
  setIdea,
  createIdea,
  onOpen,
}: {
  initiatives: Initiative[];
  idea: string;
  setIdea: (v: string) => void;
  createIdea: () => void;
  onOpen: (id: string) => void;
}) {
  return (
    <div>
      <h1 style={{ margin: 0 }}>Products</h1>
      <p className="muted">Your initiatives across this project.</p>
      <div className="row" style={{ marginTop: 12 }}>
        <input
          className="text-input"
          style={{ flex: 1 }}
          placeholder="What are you thinking about building?"
          value={idea}
          onChange={(e) => setIdea(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && createIdea()}
        />
        {idea.trim() && <button onClick={createIdea}>Start</button>}
      </div>
      <ul className="list" style={{ marginTop: 18 }}>
        {initiatives.map((i) => (
          <li key={i.id}>
            <button className="ghost" onClick={() => onOpen(i.id)}>
              <strong>{i.title}</strong> · <span className="muted">{i.status}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
