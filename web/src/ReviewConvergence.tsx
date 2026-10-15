import { useEffect, useState } from "react";
import { get, resource } from "./api";
import { ToneChip } from "./GraphBits";
import {
  blockingTrend,
  demotedSummary,
  groupLoops,
  outcomeLabel,
  outcomeTone,
  reviewTitle,
  sevSummary,
  total,
  type ReviewTelemetry,
} from "./convergence";

// ReviewConvergence is the review loop made visible: per cycle, the findings
// raised / new / re-raised / resolved by severity, the ones demoted to
// advisory and by which rule, the open blocking count, and what the loop did
// next (converged, revised, stalled, hit max cycles). Shown on Define (the PRD
// review) and Plan (the plan review), from GET .../review-telemetry. Renders
// nothing until a review has run.
export default function ReviewConvergence({
  projectId,
  initiativeId,
  review,
}: {
  projectId: string;
  initiativeId: string;
  review: "prd" | "execution_plan";
}) {
  const [tel, setTel] = useState<ReviewTelemetry[] | null>(null);

  useEffect(() => {
    let live = true;
    const load = () =>
      get<ReviewTelemetry[]>(`${resource(projectId, initiativeId, "review-telemetry")}?review=${review}`)
        .then((t) => live && setTel(t ?? []))
        .catch(() => live && setTel([]));
    load();
    // A review loop runs in the background; a slow poll keeps the timeline
    // current without the page's 2.5s one.
    const id = setInterval(load, 10000);
    return () => {
      live = false;
      clearInterval(id);
    };
  }, [projectId, initiativeId, review]);

  const loops = groupLoops(tel ?? [], review);
  if (loops.length === 0) return null;
  const latest = loops[loops.length - 1];

  return (
    <section className="rc" aria-labelledby={`rc-${review}`}>
      <div className="judgment-head">
        <h2 id={`rc-${review}`} className="section-title">
          {reviewTitle(review)} convergence
        </h2>
        <span className="muted">
          {latest.cycles.length} cycle{latest.cycles.length === 1 ? "" : "s"} · {outcomeLabel(latest.outcome)}
          {loops.length > 1 ? ` · ${loops.length} loops` : ""}
        </span>
      </div>
      {loops.map((loop, li) => (
        <details key={li} className="rc-loop" open={li === loops.length - 1}>
          <summary>
            {loops.length > 1 ? `Loop ${li + 1}` : "Cycles"} · {loop.cycles.length} cycle{loop.cycles.length === 1 ? "" : "s"} ·{" "}
            <ToneChip tone={outcomeTone(loop.outcome)}>{outcomeLabel(loop.outcome)}</ToneChip>
          </summary>
          <ol className="rc-timeline">
            {loop.cycles.map((c, i) => (
              <li key={i} className="rc-cycle">
                <div className="rc-cycle-head">
                  <span className="rc-cycle-n">Cycle {c.cycle}</span>
                  <ToneChip tone={outcomeTone(c.outcome)}>{outcomeLabel(c.outcome)}</ToneChip>
                  <span className="rc-blocking">
                    <strong>{c.open_blocking}</strong> open blocking
                    {blockingTrend(c) && <span className="muted"> ({blockingTrend(c)})</span>}
                  </span>
                  <span className="muted">
                    {c.mode}
                    {c.rigor ? ` · ${c.rigor}` : ""}
                  </span>
                </div>
                <dl className="rc-stats">
                  <div>
                    <dt>Raised</dt>
                    <dd>{total(c.raised) ? sevSummary(c.raised) : "none"}</dd>
                  </div>
                  <div>
                    <dt>New</dt>
                    <dd>{sevSummary(c.new)}</dd>
                  </div>
                  <div>
                    <dt>Re-raised</dt>
                    <dd>{sevSummary(c.reraised)}</dd>
                  </div>
                  <div>
                    <dt>Resolved</dt>
                    <dd>{sevSummary(c.resolved)}</dd>
                  </div>
                  {demotedSummary(c.demoted) && (
                    <div>
                      <dt>Demoted</dt>
                      <dd>{demotedSummary(c.demoted)}</dd>
                    </div>
                  )}
                  {c.deterministic > 0 && (
                    <div>
                      <dt>Deterministic</dt>
                      <dd>{c.deterministic} gating</dd>
                    </div>
                  )}
                </dl>
              </li>
            ))}
          </ol>
        </details>
      ))}
    </section>
  );
}
