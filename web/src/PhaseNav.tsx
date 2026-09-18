import type { PhaseName } from "./router";
import { phaseHref } from "./router";
import { currentPill, type PhaseState } from "./phases";

interface InboxItem {
  initiativeId: string;
  count: number;
}

interface Props {
  initiative: { id: string; title: string; status: string } | null;
  projectName: string;
  initiativeIndex: number; // 1-based
  initiativeCount: number;
  phases: PhaseState[];
  active: PhaseName | "settings";
  inbox: InboxItem[]; // empty → hidden
  modelChip: string;
  modelDot?: "ready" | "warn";
  onNavigate: (href: string) => void;
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
  onNavigate,
}: Props) {
  const cur = initiative ? currentPill(initiative.status) : { pill: "", note: "" };

  return (
    <aside className="phase-nav">
      <button className="pn-logo" onClick={() => onNavigate("#/")}>
        <span className="pn-logo-mark">ih</span>
        <span className="pn-logo-text">idea-harness</span>
      </button>

      {initiative && (
        <button className="pn-initiative" onClick={() => onNavigate("#/")}>
          <span className="pn-initiative-title">{initiative.title}</span>
          <span className="pn-initiative-sub">
            {projectName} · #{initiativeIndex} of {initiativeCount} ▾
          </span>
        </button>
      )}

      {inbox.length > 0 && (
        <div className="pn-inbox">
          <button className="pn-inbox-row" onClick={() => onNavigate("#/")}>
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
              return (
                <button
                  key={p.phase}
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
              );
            })}
          </nav>
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
          {/* "Activity" used to sit inside the model button and navigate to
              Settings regardless -- a label with no destination of its own.
              It now goes to this initiative's health/activity trace, and
              only appears when there is one. */}
          {initiative && (
            <button className="pn-model-more" onClick={() => onNavigate(`#/i/${initiative.id}/health`)}>
              Activity ↗
            </button>
          )}
        </div>
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
