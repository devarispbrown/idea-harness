import { useState } from "react";
import { navigate } from "./router";
import { staleBannerText, staleItemHref, staleItemLabel, type StaleItemRef } from "./staleItems";

// StaleBanner (A4 #5 / JTBD 5: "nothing flagged what a changed decision made
// invalid"). App.tsx renders it in the app shell so it is visible from every
// phase, not just the one where the stale mark happened to originate.
export default function StaleBanner({
  initiativeId,
  items,
  onAck,
}: {
  initiativeId: string;
  items: StaleItemRef[];
  onAck: (staleId: string) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  if (items.length === 0) return null;

  return (
    <div className="w3-stale-banner" role="alert">
      <div className="w3-stale-banner-head">
        <span>{staleBannerText(items)}</span>
        <button className="w3-stale-banner-toggle" onClick={() => setExpanded((v) => !v)}>
          {expanded ? "Hide" : "Show"} {items.length === 1 ? "item" : "items"}
        </button>
      </div>
      {expanded && (
        <ul className="w3-stale-list">
          {items.map((item) => {
            const href = staleItemHref(initiativeId, item.type);
            return (
              <li key={item.stale_id} className="w3-stale-item">
                <span>{staleItemLabel(item)}</span>
                <span className="w3-stale-item-actions">
                  {href && (
                    <button className="ghost" onClick={() => navigate(href)}>
                      Open
                    </button>
                  )}
                  <button className="ghost" onClick={() => onAck(item.stale_id)}>
                    Acknowledge
                  </button>
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
