import { currentPill, currentStage, nextAction, STAGE_LABELS, STAGE_PURPOSE, type PhaseSource } from "./phases";

// NowNext is the persistent "Now · Needs you · Next" strip (P0.1). It answers,
// at a glance and from any initiative view, the three questions a user actually
// has: what is happening, what is waiting on me, and what is the one next step.
//
// `phaseSource` drives the stage + next action (see phases.ts); `needsYou` is
// the current initiative's open judgment count (decisions + questions +
// findings), and `needsYouHref` links the count to the phase where those items
// live. Both are computed by App.tsx from the attention rollup.
export default function NowNext({
  phaseSource,
  needsYou,
  needsYouHref,
}: {
  phaseSource: PhaseSource;
  needsYou: number;
  needsYouHref?: string;
}) {
  const stage = currentStage(phaseSource);
  const pill = currentPill(phaseSource.status);
  const next = nextAction(phaseSource);

  const needsYouBody = (
    <>
      <span className="nn-label">Needs you</span>
      <span className="nn-count">{needsYou}</span>
    </>
  );

  return (
    <section className="now-next" aria-live="polite">
      <div className={`nn-item nn-now nn-${stage}`}>
        <span className="nn-label">Now</span>
        <span className="nn-stage">{STAGE_LABELS[stage]}</span>
        {pill.note && (
          <span className={`nn-state nn-state-${phaseSource.status}`}>
            {pill.pill} {pill.note}
          </span>
        )}
        <span className="nn-purpose">{STAGE_PURPOSE[stage]}</span>
      </div>

      {needsYouHref ? (
        <a className={`nn-item nn-needs${needsYou > 0 ? " nn-has" : ""}`} href={needsYouHref}>
          {needsYouBody}
        </a>
      ) : (
        <div className={`nn-item nn-needs${needsYou > 0 ? " nn-has" : ""}`}>{needsYouBody}</div>
      )}

      <div className="nn-item nn-next">
        <span className="nn-label">Next</span>
        <span className="nn-action">{next.action}</span>
        {next.command && <code className="nn-command">{next.command}</code>}
      </div>
    </section>
  );
}
