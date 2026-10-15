import { useCallback, useEffect, useRef, useState } from "react";
import { get, NetworkError, post, resource, UNAUTHORIZED_EVENT } from "./api";
import Settings from "./Settings";
import PhaseNav from "./PhaseNav";
import { providerDotState, type ProviderDotState, type ProviderStatusInput } from "./providerStatus";
import PhasePage from "./PhasePage";
import DefinePage from "./DefinePage";
import ExplorePage from "./ExplorePage";
import BuildPage from "./BuildPage";
import PlanPage from "./PlanPage";
import VerifyPage from "./VerifyPage";
import RunningCard from "./RunningCard";
import DocReader from "./DocReader";
import ArtifactDiffPage from "./ArtifactDiffPage";
import CommandPalette from "./CommandPalette";
import FailedBanner from "./FailedBanner";
import DesignPage from "./DesignPage";
import DecisionPage from "./DecisionPage";
import HealthPage from "./HealthPage";
import ProductHome from "./ProductHome";
import ChangedPage from "./ChangedPage";
import QueuePage from "./QueuePage";
import AutomationsPage from "./AutomationsPage";
import BudgetMeter from "./BudgetMeter";
import StaleBanner from "./StaleBanner";
import TimelinePanel from "./TimelinePanel";
import LineageView from "./LineageView";
import GapsPage from "./GapsPage";
import OutcomesPage from "./OutcomesPage";
import QualityPage from "./QualityPage";
import NotFound from "./NotFound";
import LiveAnnouncer from "./LiveAnnouncer";
import ShortcutsDialog from "./ShortcutsDialog";
import { focusTarget, useRouteFocus } from "./focus";
import type { StatusPill } from "./PhaseHeader";
import type { DocumentRowProps } from "./DocumentRow";
import {
  ARTIFACT_PHASE,
  ARTIFACT_TITLES,
  PHASE_LABELS,
  PHASE_PURPOSE,
  attentionCount,
  currentPhase,
  derivePhases,
  type InitiativeRollup,
  type PhaseSource,
  type PhaseState,
} from "./phases";
import {
  PHASES,
  currentProjectFromHash,
  currentRoute,
  navigate,
  phaseHref,
  setActiveProject,
  withProject,
  type Route,
  type PhaseName,
} from "./router";
import { hasToken, resolveAuth, type AuthState } from "./remote";
import LoginPage from "./LoginPage";
import ProjectSwitcher from "./ProjectSwitcher";
import { pickProject, rememberProject, rememberedProject } from "./projectSwitch";
import { daemonDownAfterPoll, daemonLostMessage } from "./daemonStatus";
import { verifiedRequirementCount } from "./verify";
import type { StaleItemRef } from "./staleItems";
import type { CostsView } from "./cost";
import type { TimelineEntry } from "./timeline";
import { setLastVisit } from "./lastVisit";
import { useProjectChanges } from "./useEventStream";

interface Project {
  id: string;
  name: string;
  project_id: string;
  root_path: string;
}
interface Initiative {
  id: string;
  title: string;
  // Wave 3 (C1): a short, generated title (<= 60 chars). Optional -- a
  // pre-wave-3 daemon, or a row created before this field existed, sends
  // none, and every read of it falls back to `title` via displayTitle().
  short_title?: string;
  description: string;
  status: string;
  failure_reason?: string;
  // The daemon records WHY an initiative is parked; all four reasons share the
  // `needs_human_judgment` status, so the status alone cannot tell a plan-review
  // park from a plan-dependencies one -- and the two have different escapes.
  judgment_reason?: string;
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
  // Remote sign-in (remote.ts): a page with the token is authenticated
  // already; one without asks the daemon whether its cookie is a session, and
  // shows the sign-in page if not. Nothing loads until that is settled.
  const [auth, setAuth] = useState<AuthState | "checking">(() => (hasToken() ? "token" : "checking"));
  const [signedOut, setSignedOut] = useState(false);
  const authed = auth === "token" || auth === "session";
  useEffect(() => {
    if (auth !== "checking") return;
    resolveAuth().then(setAuth);
  }, [auth]);
  useEffect(() => {
    const onUnauthorized = () => {
      setSignedOut(true);
      setAuth("signed-out");
    };
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
  }, []);

  const [projects, setProjects] = useState<Project[]>([]);
  const [projectId, setProjectId] = useState("");
  // The project the address named but the daemon does not serve (a stale
  // deep link): shown as not-found rather than silently swapped for another.
  const [unknownProject, setUnknownProject] = useState("");
  // The selected project, readable from callbacks without re-creating them.
  const projectIdRef = useRef("");
  const [initiatives, setInitiatives] = useState<Initiative[]>([]);
  const [route, setRoute] = useState<Route>(() => currentRoute());
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [changeSets, setChangeSets] = useState<ChangeSet[]>([]);
  const [defaultModel, setDefaultModel] = useState("");
  const [demo, setDemo] = useState(false);
  const [modelDot, setModelDot] = useState<ProviderDotState>("ready");
  const [attention, setAttention] = useState<InitiativeRollup[]>([]);
  const [verifiedRequirements, setVerifiedRequirements] = useState(0);
  const [initiativesLoaded, setInitiativesLoaded] = useState(false);
  // Wave 3: stale items (A4 #5), cost/time (C3) and the timeline (C1) for
  // whichever initiative is currently open. Empty/null until it loads or
  // when the initiative has none -- every consumer treats that the same as
  // "nothing to show" rather than distinguishing the two.
  const [staleItems, setStaleItems] = useState<StaleItemRef[]>([]);
  const [costs, setCosts] = useState<CostsView | null>(null);
  const [timeline, setTimeline] = useState<TimelineEntry[]>([]);

  const [daemonDown, setDaemonDown] = useState(false);
  // Focus follows the route: after a navigation, the new page's heading has
  // focus (focus.ts), so a keyboard user is not left on a link that has gone.
  const mainRef = useRef<HTMLElement>(null);
  useRouteFocus(JSON.stringify(route), mainRef);

  // M4: `daemonDown` used to be set independently by each of the four loaders
  // below, from just its own single request's outcome -- so whichever one
  // happened to resolve LAST in a given tick decided the flag, regardless of
  // what the others had just found. Two loaders firing together (a success
  // and a network failure) would flicker the daemon-lost screen on and off
  // depending purely on resolution order. `outcomesRef` keeps every loader's
  // most recent network-failure verdict, keyed by name, and every report
  // recomputes `daemonDown` from the combined set -- one decision, not four
  // independent ones. It reads "down" while ANY active poller's latest
  // attempt was a network failure; a poller that stops is removed
  // (forgetOutcome), so a stale failure cannot hold the banner up.
  const outcomesRef = useRef<Record<string, boolean>>({});
  const reportOutcome = useCallback((key: string, networkError: boolean) => {
    outcomesRef.current = { ...outcomesRef.current, [key]: networkError };
    setDaemonDown(
      daemonDownAfterPoll(Object.values(outcomesRef.current).map((networkError) => ({ networkError }))),
    );
  }, []);
  const forgetOutcome = useCallback((key: string) => {
    const { [key]: _gone, ...rest } = outcomesRef.current;
    outcomesRef.current = rest;
    setDaemonDown(daemonDownAfterPoll(Object.values(rest).map((networkError) => ({ networkError }))));
  }, []);

  // loadProjects, loadInitiatives, loadAttention and loadInitiativeData each
  // report their own success/failure into the combined decision above (H1)
  // rather than only the first, one-shot project load. Critically, none of
  // them ever writes a failure's result into state -- only a successful fetch
  // updates its piece of state, so a dead daemon can flip `daemonDown` but
  // can never overwrite the last good data with `[]` (see daemonStatus.ts).
  // Only a NetworkError (fetch got no response at all) counts as "down"; an
  // ordinary HTTP error response does not (M4).
  const loadProjects = useCallback(async () => {
    try {
      const ps = (await get<Project[]>("/projects")) ?? [];
      setProjects(ps);
      if (ps.length > 0 && !projectIdRef.current) {
        const choice = pickProject(ps, currentProjectFromHash(), rememberedProject());
        projectIdRef.current = choice.projectId;
        setUnknownProject(choice.unknown ?? "");
        setProjectId(choice.projectId);
      }
      reportOutcome("projects", false);
    } catch (e) {
      reportOutcome("projects", e instanceof NetworkError);
    }
  }, [reportOutcome]);

  const loadInitiatives = useCallback(async (pid: string) => {
    try {
      const list = (await get<Initiative[]>(`/projects/${pid}/initiatives`)) ?? [];
      setInitiatives(list);
      setInitiativesLoaded(true);
      reportOutcome("initiatives", false);
    } catch (e) {
      reportOutcome("initiatives", e instanceof NetworkError);
    }
  }, [reportOutcome]);

  const loadAttention = useCallback(async (pid: string) => {
    try {
      const res = await get<{ items: InitiativeRollup[] }>(`/projects/${pid}/attention`);
      setAttention(res.items ?? []);
      reportOutcome("attention", false);
    } catch (e) {
      reportOutcome("attention", e instanceof NetworkError);
    }
  }, [reportOutcome]);

  const loadInitiativeData = useCallback(async (pid: string, iniId: string) => {
    const settled = await Promise.allSettled([
      get<Artifact[]>(resource(pid, iniId, "artifacts")),
      get<Task[]>(`/projects/${pid}/initiatives/${iniId}/tasks`),
      get<ChangeSet[]>(resource(pid, iniId, "change-sets")),
      get<{ status: string }[]>(resource(pid, iniId, "requirements")),
    ]);
    const [a, t, c, r] = settled;
    if (a.status === "fulfilled") setArtifacts(a.value ?? []);
    if (t.status === "fulfilled") setTasks(t.value ?? []);
    if (c.status === "fulfilled") setChangeSets(c.value ?? []);
    // The sidebar's "N requirements verified" is derived from the same
    // requirements list, and the same counting function, as the Verify page
    // itself (K) -- previously this was hard-coded to 0 here and never wired.
    if (r.status === "fulfilled") setVerifiedRequirements(verifiedRequirementCount(r.value ?? []));
    // A round counts as a network failure only if one of ITS OWN rejections
    // was a NetworkError -- an ordinary HTTP error on one of the four
    // sub-requests is not evidence the daemon is gone (M4).
    reportOutcome(
      "initiative-data",
      settled.some((x) => x.status === "rejected" && x.reason instanceof NetworkError),
    );
  }, [reportOutcome]);

  // Wave 3: stale items (guidance's stale_items, A4 #5), cost/time (C3) and
  // the timeline panel (C1) -- three endpoints the surfaces worker is
  // landing alongside this UI. Same allSettled shape as loadInitiativeData
  // above: each keeps the last good value it had on any individual failure,
  // and only a NetworkError on any of the three counts as the daemon being
  // down (M4). A daemon that predates one of these endpoints answers with an
  // ordinary HTTP error, not a NetworkError, so it never flips the banner --
  // that item's state just stays empty/absent, which every consumer below
  // already renders nothing (or a degraded state) for.
  const loadWave3Data = useCallback(async (pid: string, iniId: string) => {
    const settled = await Promise.allSettled([
      get<{ stale_items?: StaleItemRef[] }>(resource(pid, iniId, "guidance")),
      get<CostsView>(resource(pid, iniId, "costs")),
      get<TimelineEntry[]>(resource(pid, iniId, "timeline")),
    ]);
    const [g, c, t] = settled;
    if (g.status === "fulfilled") setStaleItems(g.value?.stale_items ?? []);
    if (c.status === "fulfilled") setCosts(c.value ?? null);
    if (t.status === "fulfilled") setTimeline(t.value ?? []);
    reportOutcome(
      "wave3-data",
      settled.some((x) => x.status === "rejected" && x.reason instanceof NetworkError),
    );
  }, [reportOutcome]);

  useEffect(() => {
    if (authed) loadProjects();
  }, [loadProjects, authed]);

  // switchProject is the switcher's action, and what a link naming another
  // project does: everything initiative-scoped belongs to the old project, so
  // it is cleared rather than shown against the new one until it reloads.
  const switchProject = useCallback((pid: string, href?: string) => {
    rememberProject(pid);
    setUnknownProject("");
    if (projectIdRef.current !== pid) {
      setInitiatives([]);
      setInitiativesLoaded(false);
      setAttention([]);
    }
    projectIdRef.current = pid;
    setProjectId(pid);
    setActiveProject(pid);
    if (href !== undefined) navigate(withProject(href, pid));
  }, []);

  // navigate() names the current project in every link it follows.
  useEffect(() => {
    setActiveProject(projectId);
  }, [projectId]);
  useEffect(() => {
    if (!projectId) return;
    loadInitiatives(projectId);
    loadAttention(projectId);
    get<{ default_model: string; demo?: boolean; providers?: (ProviderStatusInput & { name: string })[] }>(
      `/projects/${projectId}/providers`,
    )
      .then((v) => {
        setDefaultModel(v.default_model || "");
        setDemo(v.demo ?? false);
        // The dot was always green, even with the default provider's key
        // missing -- the one state it exists to show.
        const name = (v.default_model || "").split("/")[0];
        const p = v.providers?.find((x) => x.name === name);
        setModelDot(!v.demo && p ? providerDotState(p) : "warn");
      })
      .catch(() => {});
  }, [projectId, loadInitiatives, loadAttention]);

  // Hash routing. A hash naming another project (a pasted deep link, a push
  // notification opening an existing tab) switches to it.
  useEffect(() => {
    const onHash = () => {
      setRoute(currentRoute());
      const named = currentProjectFromHash();
      if (named && projects.some((p) => p.project_id === named)) {
        if (named !== projectId) switchProject(named);
      } else if (named && projects.length > 0) {
        setUnknownProject(named);
      }
    };
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, [projects, projectId, switchProject]);

  // Canonical address: a bare `#/…` gets the current project's prefix
  // (replaceState, so Back is unaffected), so what a person copies out of the
  // address bar opens the same project for whoever follows it.
  useEffect(() => {
    if (!projectId || unknownProject) return;
    const hash = window.location.hash || "#/";
    const canonical = withProject(hash, projectId);
    if (canonical !== hash) window.history.replaceState(null, "", canonical);
  }, [projectId, route, unknownProject]);

  const initiativeId =
    route.name !== "home" &&
    route.name !== "settings" &&
    route.name !== "not-found" &&
    route.name !== "queue" &&
    route.name !== "automations"
      ? route.id
      : "";
  const currentInitiative = initiativeId
    ? initiatives.find((i) => i.id === initiativeId) ?? null
    : null;
  const currentInitiativeId = currentInitiative?.id ?? "";
  // Every route that names an initiative renders NotFound, and nothing else,
  // when the id resolves to none in this project. Only phase routes used to
  // check: a decision, health, doc, diff or changed link with a stale id
  // rendered its page against an initiative that does not exist.
  const missingInitiative = initiativeId !== "" && initiativesLoaded && !currentInitiative;

  useEffect(() => {
    if (projectId && currentInitiativeId) {
      loadInitiativeData(projectId, currentInitiativeId);
      loadWave3Data(projectId, currentInitiativeId);
    }
    // A poller that stops must stop voting. Leaving the initiative kept its
    // last "network error" in the combined decision, so after the daemon came
    // back the banner stayed up until the initiative was opened again.
    return () => {
      forgetOutcome("initiative-data");
      forgetOutcome("wave3-data");
    };
  }, [projectId, currentInitiativeId, loadInitiativeData, loadWave3Data, forgetOutcome]);

  // "Since you were last here" (C1): record this visit's end so, next time
  // Home is viewed, entriesSince() can show what changed after that moment.
  // ProductHome reads the cursor's PREVIOUS value before this effect ever
  // runs, since it can only be on Home while currentInitiativeId is empty.
  //
  // M7, review fix: stamps on cleanup (leaving this initiative, whether by
  // navigating away or unmounting), with a fresh timestamp, so everything
  // that happened DURING a visit -- the user's own actions, a run finishing
  // while they watched -- correctly counts as seen too.
  //
  // LOW fix (release-gate report): this used to ALSO stamp on entry, which
  // consumed the unread cursor immediately on arrival, before anything had
  // actually been shown. Opening an initiative and leaving a moment later --
  // a misclick, a wrong initiative in a long list -- advanced the cursor to
  // "now" regardless, so every entry between the previous visit and that
  // instant was irrecoverably marked seen even though nothing was read. The
  // exit-only stamp still records "everything up to when I looked away",
  // whatever the visit's length, without that false-positive on a fast
  // in-and-out.
  useEffect(() => {
    return () => {
      if (currentInitiativeId) setLastVisit(currentInitiativeId, new Date().toISOString());
    };
  }, [currentInitiativeId]);

  // Freshness: refetch when the project's event stream (SSE) says something
  // changed -- the What-changed and queue badges, the initiative's data. The
  // timer stays as a backstop: slow while the stream is live, the old 2.5s
  // poll when it is not (no EventSource, a proxy that strips it).
  const refresh = useCallback(() => {
    if (!projectId) return;
    loadInitiatives(projectId);
    loadAttention(projectId);
    if (currentInitiativeId) {
      loadInitiativeData(projectId, currentInitiativeId);
      loadWave3Data(projectId, currentInitiativeId);
    }
  }, [projectId, currentInitiativeId, loadInitiatives, loadAttention, loadInitiativeData, loadWave3Data]);
  const projectStreamLive = useProjectChanges(projectId ?? "", refresh);
  useEffect(() => {
    if (!projectId) return;
    const id = setInterval(refresh, projectStreamLive ? 15000 : 2500);
    return () => clearInterval(id);
  }, [projectId, refresh, projectStreamLive]);

  const phaseSource: PhaseSource = {
    status: currentInitiative?.status ?? "",
    judgmentReason: currentInitiative?.judgment_reason,
    artifacts,
    tasks,
    changeSets,
    verifiedRequirements,
  };
  const phases = derivePhases(phaseSource);
  const activePhase: PhaseName | "settings" | "automations" =
    route.name === "phase"
      ? route.phase
      : route.name === "settings"
        ? "settings"
        : route.name === "automations"
          ? "automations"
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

  // ackStale (A4 #5): clears one stale mark. Optimistic removal so the
  // banner updates immediately; a refusal (already cleared, wrong
  // initiative) just gets picked up on the next poll rather than a visible
  // error for what is, at worst, a stale click.
  const ackStale = async (staleId: string) => {
    if (!projectId || !currentInitiativeId) return;
    setStaleItems((items) => items.filter((i) => i.stale_id !== staleId));
    try {
      await post(resource(projectId, currentInitiativeId, `stale/${staleId}/ack`), {});
    } catch {
      loadWave3Data(projectId, currentInitiativeId);
    }
  };

  // ---------- render ----------

  if (auth === "checking") {
    return (
      <div className="empty">
        <p className="muted">Connecting…</p>
      </div>
    );
  }
  if (auth === "signed-out") {
    return (
      <LoginPage
        expired={signedOut}
        onSignedIn={() => {
          setSignedOut(false);
          setAuth("session");
        }}
      />
    );
  }
  if (auth === "unreachable") {
    return (
      <div className="empty">
        <h1>idea-harness</h1>
        <p className="error">{daemonLostMessage(window.location.host)}</p>
        <button className="cs-secondary" onClick={() => setAuth("checking")}>
          Try again
        </button>
      </div>
    );
  }

  // M4/H1: with no projects loaded yet there is no "last good page" to keep
  // showing underneath a banner, so this is the one place daemonDown still
  // replaces the whole screen -- and even then, only to say WHY there's
  // nothing (unreachable daemon) rather than the misleading "no projects
  // yet" a brand-new install would otherwise show for the same blank state.
  if (projects.length === 0) {
    return (
      <div className="empty">
        <h1>idea-harness</h1>
        {daemonDown ? (
          <>
            <p className="error">{daemonLostMessage(window.location.host)}</p>
            <p className="muted">Nothing will update until it's back. Run <code>idea daemon</code> to restart it.</p>
          </>
        ) : (
          <p className="muted">
            No projects yet. Run <code>idea init</code> in a project directory to get started.
          </p>
        )}
      </div>
    );
  }

  // From here on there IS a last-good page to preserve: a network failure
  // renders as a banner overlaid on top of it (below), never a full-page
  // replacement that would throw away every initiative/phase/artifact
  // already on screen the moment a single poll fails.

  const project = projects.find((p) => p.project_id === projectId);
  const initiativeIndex = currentInitiative
    ? initiatives.findIndex((i) => i.id === currentInitiative.id) + 1
    : 1;

  // Sidebar inbox: open items in initiatives OTHER than the one being viewed.
  // Only shown when a specific initiative is focused ("elsewhere" is meaningless
  // on the home/settings screens). attentionCount excludes a COMPLETE
  // initiative's open items -- once shipped they're optional cleanup, not
  // something waiting on the person (see phases.ts).
  const inbox = currentInitiativeId
    ? attention
        .filter((r) => r.id !== currentInitiativeId)
        .map((r) => ({
          initiativeId: r.id,
          count: attentionCount(r),
        }))
        .filter((i) => i.count > 0)
    : [];
  // Everything waiting on the person across the project: the way into the
  // queue from Home and Settings, and from the top bar on a phone.
  const needsYou = attention.reduce((n, r) => n + attentionCount(r), 0);

  return (
    <div className="app-root">
      {/* The first Tab stop on every page: past the sidebar's dozen links
          straight to the page itself. Its click is handled here, because a
          real "#main-content" href would be read as a route by the hash
          router. */}
      <a
        className="skip-link"
        href="#main-content"
        onClick={(e) => {
          e.preventDefault();
          if (mainRef.current) focusTarget(mainRef.current);
        }}
      >
        Skip to content
      </a>
      <LiveAnnouncer />
      {daemonDown && (
        <div className="daemon-down-banner" role="alert">
          {daemonLostMessage(window.location.host)} Nothing will update until it's back — run{" "}
          <code>idea daemon</code> to restart it.
        </div>
      )}
      {demo && (
        <div className="banner banner-app">
          Demo mode — no model is selected for this project, so every document,
          review and change you see here is synthetic.{" "}
          <a
            href="#/settings"
            onClick={(e) => {
              e.preventDefault();
              navigate("#/settings");
            }}
          >
            Configure a provider in Settings → Providers
          </a>
          .
        </div>
      )}
      <div className="app-shell">
        <PhaseNav
          initiative={currentInitiative}
          projectName={project?.name ?? ""}
          initiativeIndex={initiativeIndex}
          initiativeCount={initiatives.length}
          phases={phases}
          active={activePhase}
          inbox={inbox}
          modelChip={defaultModel ? defaultModel.split("/").pop() || defaultModel : "demo"}
          modelDot={modelDot}
          costs={costs}
          graphView={route.name === "gaps" || route.name === "outcomes" || route.name === "quality" ? route.name : undefined}
          onNavigate={navigate}
          needsYou={needsYou}
          switcher={
            <ProjectSwitcher
              projects={projects}
              projectId={projectId}
              onSwitch={(pid) => switchProject(pid, "#/")}
            />
          }
        />

        <main className="phase-main" id="main-content" ref={mainRef} tabIndex={-1}>
          {/* A4 #5 / JTBD 5: visible from every phase, not only the one
              where the stale mark happened to originate. */}
          {currentInitiativeId && staleItems.length > 0 && (
            <StaleBanner initiativeId={currentInitiativeId} items={staleItems} onAck={ackStale} />
          )}

          {currentInitiativeId && currentInitiative?.status === "failed" && (
            <FailedBanner
              projectId={projectId}
              initiativeId={currentInitiativeId}
              reason={currentInitiative.failure_reason}
              defaultModel={defaultModel || "idea-harness-default"}
            />
          )}

          {currentInitiativeId && (
            <RunningCard projectId={projectId} initiativeId={currentInitiativeId} />
          )}

          {/* The spending-cap meter, on every phase -- only when a cap
              applies or the initiative is stopped at one (BudgetMeter). */}
          {currentInitiativeId && (
            <BudgetMeter projectId={projectId} initiativeId={currentInitiativeId} variant="shell" />
          )}

          {/* C1 / JTBD 6: "no useful 'what changed'" -- a compact panel on
              every phase, with a link to the full history. */}
          {currentInitiativeId && timeline.length > 0 && (
            <TimelinePanel initiativeId={currentInitiativeId} entries={timeline} />
          )}

          {route.name === "home" && (
            <ProductHome
              projectId={projectId}
              initiatives={initiatives}
              attention={attention}
              projectName={project?.name ?? ""}
              onOpen={(id) => navigate(`#/i/${id}/explore`)}
              onCreate={createIdea}
              onOpenSettings={() => navigate("#/settings")}
            />
          )}

        {route.name === "settings" && <Settings projectId={projectId} />}

        {unknownProject && (
          <NotFound
            title="Project not found"
            detail={`This daemon serves no project "${unknownProject}". Pick one from the project switcher.`}
          />
        )}

        {missingInitiative && !unknownProject && (
          <NotFound
            title="Initiative not found"
            detail={`There's no initiative "${initiativeId}" in this project.`}
          />
        )}

        {route.name === "not-found" && (
          <NotFound title="Page not found" detail={`There's nothing at #/${route.path}.`} />
        )}

        {route.name === "phase" && currentInitiative && route.phase === "define" && (
          <DefinePage
            projectId={projectId}
            initiativeId={currentInitiative.id}
            status={currentInitiative.status}
            judgmentReason={currentInitiative.judgment_reason}
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
            judgmentReason={currentInitiative.judgment_reason}
          />
        )}
        {route.name === "phase" && currentInitiative && route.phase === "design" && (
          <DesignPage
            projectId={projectId}
            initiativeId={currentInitiative.id}
            pill={phasePill(phases.find((p) => p.phase === "design"), currentInitiative.status, "design")}
            docs={artifactRows(artifacts, currentInitiative.id, "design")}
            canReopen={artifacts.some((a) => a.kind === "execution_plan") && changeSets.length === 0}
          />
        )}
        {route.name === "phase" && currentInitiative && route.phase === "verify" && (
          <VerifyPage projectId={projectId} initiativeId={currentInitiative.id} />
        )}
        {route.name === "phase" && currentInitiative && route.phase !== "define" && route.phase !== "explore" && route.phase !== "build" && route.phase !== "plan" && route.phase !== "design" && route.phase !== "verify" && (
          <PhasePage
            title={PHASE_LABELS[route.phase]}
            purpose={PHASE_PURPOSE[route.phase]}
            pill={phasePill(phases.find((p) => p.phase === route.phase), currentInitiative.status, route.phase)}
            docs={artifactRows(artifacts, currentInitiative.id, route.phase)}
          >
            <div className="phase-stub muted">Phase content lands in a later step.</div>
          </PhasePage>
        )}

        {route.name === "phase" &&
          currentInitiative &&
          (() => {
            // The handoff link: once the phase being viewed has actually
            // completed (per the SAME single-source rail as the sidebar, not
            // a re-derivation), point at the next one. Verify is terminal --
            // there is nothing after it.
            const state = phases.find((p) => p.phase === route.phase);
            if (state?.state !== "done") return null;
            const idx = PHASES.indexOf(route.phase);
            const next = PHASES[idx + 1];
            if (!next) return null;
            return (
              <div className="phase-handoff">
                <button
                  className="phase-handoff-link"
                  onClick={() => navigate(phaseHref(currentInitiative.id, next))}
                >
                  Next: {PHASE_LABELS[next]} →
                </button>
              </div>
            );
          })()}

        {route.name === "decision" && currentInitiative && (
          <DecisionPage projectId={projectId} initiativeId={route.id} decisionId={route.decisionId} />
        )}
        {route.name === "health" && currentInitiative && (
          <HealthPage projectId={projectId} initiativeId={route.id} />
        )}
        {route.name === "doc" && currentInitiative && (
          <DocReader projectId={projectId} initiativeId={route.id} artifactId={route.artifact} />
        )}
        {route.name === "diff" && currentInitiative && (
          <ArtifactDiffPage projectId={projectId} from={route.from} to={route.to} />
        )}
        {route.name === "changed" && currentInitiative && (
          <ChangedPage projectId={projectId} initiativeId={route.id} />
        )}
        {route.name === "queue" && <QueuePage projectId={projectId} />}
        {route.name === "automations" && <AutomationsPage projectId={projectId} />}
        {/* The Product Graph views (lineage, gaps, outcomes, quality). */}
        {route.name === "lineage" && (
          <LineageView projectId={projectId} initiativeId={route.id} nodeType={route.nodeType} nodeRefParam={route.ref} />
        )}
        {route.name === "gaps" && <GapsPage projectId={projectId} initiativeId={route.id} />}
        {route.name === "outcomes" && <OutcomesPage projectId={projectId} initiativeId={route.id} />}
        {route.name === "quality" && <QualityPage projectId={projectId} initiativeId={route.id} />}
        </main>

        <CommandPalette initiativeId={currentInitiativeId} onNavigate={navigate} />
        <ShortcutsDialog />
      </div>
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
