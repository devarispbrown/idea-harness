import { useEffect, useState } from "react";
import { derivePhases, PHASE_LABELS, PHASE_PURPOSE, rollupPhaseSource } from "./phases";
import type { InitiativeRollup } from "./phases";
import { PHASES, type PhaseName } from "./router";
import { get, resource } from "./api";
import { displayTitle } from "./title";
import { entriesSince, getLastVisit } from "./lastVisit";
import { describeTimelineEntry, type TimelineEntry } from "./timeline";

interface Initiative {
  id: string;
  title: string;
  // Wave 3 (C1): see title.ts's displayTitle -- this was the ICP finding
  // that "Home titles were the whole 600-character prompt."
  short_title?: string;
  description: string;
  status: string;
}

// "shipped" asserted every complete initiative was a real deploy, the same
// overclaim buildLogic.ts's appliedOutcomeLabel exists to avoid on Build's
// own change-set pills (G3) -- a demo/placeholder-executor initiative that
// never touched anything real read exactly like one that did. ProductHome's
// rollup carries no per-change-set model/executor detail (that lives on
// BuildPage's own change-set list), so there is no cheap way to tell the two
// apart here; "Complete" claims only what's actually known at this level.
const STATUS_PILL: Record<string, { label: string; state: string }> = {
  running: { label: "running", state: "running" },
  needs_human_judgment: { label: "needs you", state: "needs-you" },
  approved: { label: "approved", state: "verified" },
  plan_approved: { label: "plan approved", state: "verified" },
  complete: { label: "Complete", state: "verified" },
  failed: { label: "failed", state: "failing" },
};

export default function ProductHome({
  projectId,
  initiatives,
  attention,
  projectName,
  onOpen,
  onCreate,
  onOpenSettings,
}: {
  projectId: string;
  initiatives: Initiative[];
  attention: InitiativeRollup[];
  projectName: string;
  onOpen: (id: string) => void;
  onCreate: (idea: string) => void;
  onOpenSettings: () => void;
}) {
  const [idea, setIdea] = useState("");
  // Shown once, after an empty "+ New idea" / Enter, until the field has
  // something in it again -- clicking with nothing typed used to just do
  // nothing (K).
  const [ideaHint, setIdeaHint] = useState(false);
  const rollupById = new Map(attention.map((r) => [r.id, r]));

  const needYou = initiatives.filter((i) => i.status === "needs_human_judgment").length;

  // "Since you were last here" (C1 / JTBD 6). For every initiative that has
  // a recorded last-visit cursor (lastVisit.ts, localStorage), fetch its
  // timeline since that cursor -- App.tsx advances the cursor to "now" the
  // moment that initiative is actually opened, so this always reads the
  // PREVIOUS visit's cutoff, never the one about to be set. A first-ever
  // visit has no cursor and is skipped: there is nothing to compare against,
  // and showing "everything" would read as a huge, undifferentiated backlog
  // rather than "what's new".
  const [sinceLastVisit, setSinceLastVisit] = useState<Record<string, TimelineEntry[]>>({});
  const [expandedSince, setExpandedSince] = useState<Record<string, boolean>>({});
  const initiativeIds = initiatives.map((i) => i.id).join(",");
  useEffect(() => {
    if (!projectId || !initiatives.length) return;
    let cancelled = false;
    (async () => {
      const entries: [string, TimelineEntry[]][] = await Promise.all(
        initiatives.map(async (i): Promise<[string, TimelineEntry[]]> => {
          const since = getLastVisit(i.id);
          if (!since) return [i.id, []];
          try {
            const list = await get<TimelineEntry[]>(
              `${resource(projectId, i.id, "timeline")}?since=${encodeURIComponent(since)}`,
            );
            // Defensive re-filter (entriesSince): the backend's `?since=` is
            // already the source of truth, but this is the same pure
            // boundary check unit-tested in lastVisit.test.ts, applied here
            // too rather than trusting an unfamiliar query param blindly.
            return [i.id, entriesSince(list ?? [], since)];
          } catch {
            return [i.id, []];
          }
        }),
      );
      if (cancelled) return;
      const map: Record<string, TimelineEntry[]> = {};
      for (const [id, list] of entries) if (list.length > 0) map[id] = list;
      setSinceLastVisit(map);
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- initiativeIds is the stable key for `initiatives`' identity.
  }, [projectId, initiativeIds]);

  const submitIdea = () => {
    if (idea.trim()) onCreate(idea.trim());
    else setIdeaHint(true);
  };

  return (
    <div className="product-home">
      <header className="ph-topbar">
        <span className="pn-logo-mark">ih</span>
        {/* The literal string "my-product" used to show here regardless of
            which project was open. */}
        <span className="ph-project">{projectName || "Project"}</span>
        <button className="ph-settings" onClick={onOpenSettings}>
          Settings
        </button>
        <button className="ph-new" onClick={submitIdea}>+ New idea</button>
      </header>
      {/* A single <main> landmark per page -- App.tsx already renders one
          around this component, so this is a plain div (K). */}
      <div className="ph-main">
        <div className="ph-head">
          <h1 className="ph-title">Initiatives</h1>
          <span className="muted">{initiatives.length} total</span>
          {needYou > 0 && <span className="ph-need">{needYou} need you</span>}
        </div>
        <div className="ph-create">
          <input
            className="text-input"
            placeholder="What are you thinking about building?"
            value={idea}
            onChange={(e) => {
              setIdea(e.target.value);
              if (ideaHint) setIdeaHint(false);
            }}
            onKeyDown={(e) => e.key === "Enter" && submitIdea()}
          />
          {ideaHint && (
            <p className="ph-create-hint">
              Type what you're thinking about building, then press Enter — e.g. "Add CSV export to
              the reports page".
            </p>
          )}
        </div>
        {initiatives.length === 0 ? (
          <ProductHomeExplainer />
        ) : (
          <div className="ph-list">
            {initiatives.map((i, idx) => {
              const pill = STATUS_PILL[i.status];
              const rollup = rollupById.get(i.id);
              const phases: { phase: PhaseName; state: string }[] = rollup
                ? derivePhases(rollupPhaseSource(rollup)).map((s) => ({ phase: s.phase, state: s.state }))
                : [];
              const since = sinceLastVisit[i.id] ?? [];
              const isExpanded = !!expandedSince[i.id];
              return (
                <section key={i.id} className="ph-card" onClick={() => onOpen(i.id)}>
                  <div className="ph-card-head">
                    <span className="ph-num">#{idx + 1}</span>
                    {/* C1 / JTBD 6: "Home titles were the whole 600-character
                        prompt." The full idea is still available as the
                        card's description; this is just the title shown. */}
                    <span className="ph-card-title">{displayTitle(i)}</span>
                    {pill && <span className={`phase-pill pill-${pill.state}`}>{pill.label}</span>}
                  </div>
                  <div className="ph-bars">
                    {phases.map((p) => (
                      <div key={p.phase} className="ph-bar">
                        <span className={`ph-bar-fill ${p.state}`} />
                        <span className={`ph-bar-label ${p.state}`}>{PHASE_LABELS[p.phase]}</span>
                      </div>
                    ))}
                  </div>
                  {since.length > 0 && (
                    <button
                      className="w3-since-last"
                      onClick={(e) => {
                        e.stopPropagation();
                        setExpandedSince((m) => ({ ...m, [i.id]: !m[i.id] }));
                      }}
                    >
                      {since.length} update{since.length === 1 ? "" : "s"} since you were last here
                      {isExpanded ? " ▾" : " ▸"}
                    </button>
                  )}
                  {since.length > 0 && isExpanded && (
                    <ul className="w3-timeline-list" onClick={(e) => e.stopPropagation()}>
                      {since.map((entry, ei) => (
                        <li key={ei} className="w3-timeline-row">
                          <span>{describeTimelineEntry(entry)}</span>
                        </li>
                      ))}
                    </ul>
                  )}
                </section>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}

// ProductHomeExplainer is the first-run empty state (G4): before this, an
// empty project showed nothing but "No initiatives yet — start one above.",
// with no explanation of what the six phases mean or what happens next.
function ProductHomeExplainer() {
  return (
    <div className="ph-empty">
      <p className="ph-empty-lead">
        Every idea moves through the same six phases, in order, from a rough idea to something
        verified against its own requirements:
      </p>
      <ol className="ph-empty-phases">
        {PHASES.map((phase) => (
          <li key={phase} className="ph-empty-phase">
            <span className="ph-empty-phase-name">{PHASE_LABELS[phase]}</span>
            <span className="ph-empty-phase-purpose">{PHASE_PURPOSE[phase]}</span>
          </li>
        ))}
      </ol>
      <p className="ph-empty-next">
        <strong>Next:</strong> type what you're thinking about building above, then press Enter.
      </p>
    </div>
  );
}
