import { useCallback, useEffect, useState } from "react";
import { get } from "./api";
import { navigate } from "./router";
import { queueItemText, queueKindBadge, sortByRank, type QueueItem } from "./queue";

// QueuePage is the "Needs you" view across initiatives (D-b): "Nothing
// showed 'what needs me' across initiatives." Project-scoped, reached from
// PhaseNav's "Needs you elsewhere N" row and (when there is nothing to
// scope to a single initiative) directly from Home.
export default function QueuePage({ projectId }: { projectId: string }) {
  const [items, setItems] = useState<QueueItem[] | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const list = await get<QueueItem[]>(`/projects/${projectId}/queue`);
      setItems(list ?? []);
      setError("");
    } catch (e) {
      // Wave 3 backend work landing alongside this UI -- degrade to an
      // explanatory empty state rather than a raw fetch error.
      setItems([]);
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [projectId]);

  useEffect(() => {
    load();
    const id = setInterval(load, 5000);
    return () => clearInterval(id);
  }, [load]);

  const ranked = sortByRank(items ?? []);

  return (
    <div className="phase-page">
      <div className="phase-header">
        <h1 className="phase-title">Needs you</h1>
        <span className="phase-purpose">Across every initiative, highest impact first.</span>
      </div>
      <button className="ghost" onClick={() => navigate("#/")}>
        ← Back to initiatives
      </button>

      {items === null && <p className="muted">Loading…</p>}
      {items !== null && ranked.length === 0 && (
        <p className="muted">
          Nothing needs you right now.{error && " (This daemon may not have the queue endpoint yet.)"}
        </p>
      )}
      {ranked.length > 0 && (
        <ul className="w3-queue-list">
          {ranked.map((item, i) => (
            // review Low fix: a real <button>, not a bare <li onClick> --
            // this row was invisible to keyboard navigation and screen
            // readers (no role, no tabIndex, no key handler).
            <li key={`${item.initiative_id}-${item.kind}-${i}`}>
              <button type="button" className="w3-queue-row" onClick={() => navigate(item.href)}>
                <div>
                  <span className="idchip chip-inactive">{queueKindBadge(item.kind)}</span>{" "}
                  <span className="w3-queue-title">{queueItemText(item)}</span>
                </div>
                <span className="muted">#{item.rank}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
