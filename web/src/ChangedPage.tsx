import { useCallback, useEffect, useState } from "react";
import { get, resource } from "./api";
import { describeTimelineEntry, groupByDay, relativeTime, timelineEntryHref, type TimelineEntry } from "./timeline";
import { navigate } from "./router";

// ChangedPage is the full "What changed" view (C1 / JTBD 6: "no useful
// 'what changed'"). The compact version of the same data is App.tsx's
// TimelinePanel, shown in the app shell; this is the dedicated page reached
// from it ("See full history").
export default function ChangedPage({
  projectId,
  initiativeId,
}: {
  projectId: string;
  initiativeId: string;
}) {
  const [entries, setEntries] = useState<TimelineEntry[] | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const list = await get<TimelineEntry[]>(resource(projectId, initiativeId, "timeline"));
      setEntries(list ?? []);
      setError("");
    } catch (e) {
      // Wave 3 backend work landing alongside this UI -- a daemon that
      // doesn't have the endpoint yet degrades to an explanatory empty
      // state rather than a raw fetch error.
      setEntries([]);
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
  }, [load]);

  const groups = groupByDay(entries ?? []);
  const now = new Date();

  return (
    <div className="phase-page">
      <div className="phase-header">
        <h1 className="phase-title">What changed</h1>
        <span className="phase-purpose">
          Change sets applied, rejected and denied, decisions changed, and what they made stale —
          in order.
        </span>
      </div>
      <button className="ghost" onClick={() => navigate(`#/i/${initiativeId}/explore`)}>
        ← Back to the initiative
      </button>

      {entries === null && <p className="muted">Loading…</p>}
      {entries !== null && entries.length === 0 && (
        <p className="muted">
          Nothing recorded yet.{error && " (This daemon may not have the timeline endpoint yet.)"}
        </p>
      )}
      {groups.map((g) => (
        <div key={g.day} className="w3-timeline-day">
          <h4>{g.day}</h4>
          <ul className="w3-timeline-list">
            {g.entries.map((e, i) => {
              // review Low fix: the backend's own `ref` names exactly what
              // this entry was about; a native <button> (not a bare <li
              // onClick>) makes it keyboard- and screen-reader-reachable,
              // and a ref with nowhere to link to (e.g. "operation") stays
              // plain text instead of a dead click target.
              const href = timelineEntryHref(initiativeId, e.ref);
              return (
                <li key={`${g.day}-${i}`} className="w3-timeline-row">
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
        </div>
      ))}
    </div>
  );
}
