import { Fragment, type ReactNode } from "react";
import type { PhaseName } from "./router";
import { phaseHref } from "./router";
import { currentPill, PHASE_TO_STAGE, STAGE_LABELS, type PhaseState } from "./phases";
import { displayTitle } from "./title";
import CostLine from "./CostLine";
import GraphNavLinks from "./GraphNavLinks";
import type { CostsView } from "./cost";

interface InboxItem {
  initiativeId: string;
  count: number;
}

interface Props {
  initiative: { id: string; title: string; short_title?: string; status: string } | null;
  projectName: string;
  initiativeIndex: number; // 1-based
  initiativeCount: number;
  phases: PhaseState[];
  active: PhaseName | "settings" | "automations" | "runs";
  inbox: InboxItem[]; // empty → hidden
  modelChip: string;
  modelDot?: "ready" | "warn";
  // C3: the compact spend-and-time line, omitted (no line shown) while it
  // hasn't loaded or the daemon doesn't have the endpoint yet.
  costs?: CostsView | null;
  // Which Product Graph view (gaps/outcomes/quality) is open, if any.
  graphView?: string;
  onNavigate: (href: string) => void;
  // Everything waiting on the person across the project (the "Needs you"
  // queue), shown when no initiative is focused -- Home, Settings, the queue
  // itself -- which on a phone is the top bar.
  needsYou?: number;
  // The project switcher (ProjectSwitcher); renders nothing with one project.
  switcher?: ReactNode;
}

export default function PhaseNav({
  initiative,
  projectName,
  initiativeIndex,
  initiativeCount,
  phases,
  active,
  inbox,
  modelChip,
  modelDot = "ready",
  costs,
  graphView,
  onNavigate,
  needsYou = 0,
  switcher,
}: Props) {
  const cur = initiative ? currentPill(initiative.status) : { pill: "", note: "" };

  return (
    <aside className="phase-nav">
      <button className="pn-logo" onClick={() => onNavigate("#/")}>
        <span className="pn-logo-mark">ih</span>
        <span className="pn-logo-text">idea-harness</span>
      </button>

      {switcher}

      {!initiative && needsYou > 0 && (
        <button className="pn-needs-you" onClick={() => onNavigate("#/queue")}>
          <span>Needs you</span>
          <span className="pn-inbox-count">{needsYou}</span>
        </button>
      )}

      {initiative && (
        <>
          <button className="pn-initiative" onClick={() => onNavigate("#/")}>
            <span className="pn-initiative-title">{displayTitle(initiative)}</span>
            <span className="pn-initiative-sub">
              {projectName} · #{initiativeIndex} of {initiativeCount} ▾
            </span>
          </button>
          <CostLine costs={costs} />
        </>
      )}

      {inbox.length > 0 && (
        <div className="pn-inbox">
          {/* D-b: this used to go back to Home, which showed the inbox
              count but no way to act on any of it. It now opens the
              cross-initiative "Needs you" queue those items rank into. */}
          <button className="pn-inbox-row" onClick={() => onNavigate("#/queue")}>
            <span className="pn-inbox-label">Needs you elsewhere</span>
            <span className="pn-inbox-count">
              {inbox.reduce((n, i) => n + i.count, 0)}
            </span>
          </button>
        </div>
      )}

      {initiative && (
        <>
          <div className="pn-eyebrow">Phases</div>

          <nav className="pn-rail">
            {phases.map((p, i) => {
              const on = active === p.phase;
              const isLast = i === phases.length - 1;
              const live = p.state === "current" ? cur : null;
              // P0.1: group the six phases under the goal-shaped stages. A
              // stage header appears before the first phase of each stage
              // (Shape = Explore/Define/Design, Build = Plan/Build, Ship =
              // Verify). Decide has no phase list — it is a state, shown as the
              // live "waiting on you" pill, not a rail entry.
              const stage = PHASE_TO_STAGE[p.phase];
              const newStage = i === 0 || PHASE_TO_STAGE[phases[i - 1].phase] !== stage;
              return (
                <Fragment key={p.phase}>
                  {newStage && <div className="pn-stage">{STAGE_LABELS[stage]}</div>}
                  <button
                    className={`pn-phase ${p.state} ${on ? "on" : ""}`}
                    onClick={() => onNavigate(phaseHref(initiative.id, p.phase))}
                  >
                    <span className="pn-dot-wrap">
                      <span className="pn-dot">{p.state === "done" ? "✓" : p.state === "current" ? "▶" : ""}</span>
                      {!isLast && <span className="pn-line" />}
                    </span>
                    <span className="pn-phase-body">
                      <span className="pn-phase-row">
                        <span className="pn-phase-label">{p.label}</span>
                        {p.state === "current" && live && live.pill && (
                          <span className={`pn-pill state-${p.state}`}>{live.pill}</span>
                        )}
                      </span>
                      {p.state === "done" && <span className="pn-note">{p.summary}</span>}
                      {p.state === "current" && live && live.note && <span className="pn-note live">{live.note}</span>}
                    </span>
                  </button>
                </Fragment>
              );
            })}
          </nav>
          <GraphNavLinks initiativeId={initiative.id} active={graphView} onNavigate={onNavigate} />
        </>
      )}

      <div className="pn-footer">
        <div className="pn-model">
          <button className="pn-model-btn" onClick={() => onNavigate("#/settings")}>
            <span
              className={modelDot === "warn" ? "pn-model-dot pn-model-dot--warn" : "pn-model-dot"}
              title={modelDot === "warn" ? "The default model's provider is not ready (demo output)" : "Provider ready"}
            />
            <span className="pn-model-name">{modelChip}</span>
          </button>
          {/* Activity is the initiative's timeline -- what changed, newest
              first (C1). It used to open Graph health, which is not activity,
              while a second "What changed" link beside it did what its label
              promised; health keeps its own link instead. */}
          {initiative && (
            <button
              className="pn-model-more"
              title="What changed in this initiative"
              onClick={() => onNavigate(`#/i/${initiative.id}/changed`)}
            >
              Activity ↗
            </button>
          )}
          {initiative && (
            <button className="pn-model-more" onClick={() => onNavigate(`#/i/${initiative.id}/health`)}>
              Health ↗
            </button>
          )}
        </div>
        <button
          className={`pn-settings pn-automations ${active === "automations" ? "on" : ""}`}
          onClick={() => onNavigate("#/automations")}
        >
          <span>Automations</span>
        </button>
        <button
          className={`pn-settings pn-automations ${active === "runs" ? "on" : ""}`}
          onClick={() => onNavigate("#/runs")}
        >
          <span>Remote runs</span>
        </button>
        <button
          className={`pn-settings ${active === "settings" ? "on" : ""}`}
          onClick={() => onNavigate("#/settings")}
        >
          <span>Settings</span>
          <span className="pn-kbd">⌘K</span>
        </button>
      </div>
    </aside>
  );
}
