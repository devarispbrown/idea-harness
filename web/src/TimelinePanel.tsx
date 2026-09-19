import { describeTimelineEntry, relativeTime, timelineEntryHref, type TimelineEntry } from "./timeline";
import { navigate } from "./router";

const PANEL_SIZE = 5;

// TimelinePanel is the compact "what changed" summary shown in the app
// shell, on every phase of an initiative (C1 / JTBD 6). The full history
// lives on ChangedPage, one click away via "See full history".
export default function TimelinePanel({
  initiativeId,
  entries,
}: {
  initiativeId: string;
  entries: TimelineEntry[];
}) {
  if (entries.length === 0) return null;
  const now = new Date();
  const recent = entries.slice(0, PANEL_SIZE);

  return (
    <div className="w3-timeline-panel">
      <h3>What changed</h3>
      <ul className="w3-timeline-list">
        {recent.map((e, i) => {
          // review Low fix: link an entry to what it was about (its `ref`)
          // when there's somewhere to send it; a real <button> rather than
          // a bare <li onClick> keeps it keyboard- and screen-reader-usable.
          const href = timelineEntryHref(initiativeId, e.ref);
          return (
            <li key={i} className="w3-timeline-row">
              {href ? (
                <button type="button" className="w3-timeline-link" onClick={() => navigate(href)}>
                  {describeTimelineEntry(e)}
                </button>
              ) : (
                <span>{describeTimelineEntry(e)}</span>
              )}
              <span className="w3-timeline-when">{relativeTime(e.at, now)}</span>
            </li>
          );
        })}
      </ul>
      <button className="w3-since-last" onClick={() => navigate(`#/i/${initiativeId}/changed`)}>
        See full history →
      </button>
    </div>
  );
}
