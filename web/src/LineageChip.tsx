import { useEffect, useRef, useState } from "react";
import { get } from "./api";

interface TraceItem {
  edge_type: string;
  from_type: string;
  from_id: string;
  from_label?: string;
  to_type: string;
  to_id: string;
  to_label?: string;
}

function nodeLabel(it: { from_label?: string; from_type: string; from_id: string }) {
  return it.from_label || `${it.from_type} ${it.from_id.slice(0, 8)}`;
}

// LineageChip is a focusable ID chip that opens a keyboard-reachable popover
// showing the node's provenance ("depends on") and dependents ("blocks N"). It
// fetches lazily on first open.
export default function LineageChip({
  label,
  className,
  projectId,
  initiativeId,
  nodeType,
  nodeId,
}: {
  label: string;
  className: string;
  projectId: string;
  initiativeId: string;
  nodeType: string;
  nodeId: string;
}) {
  const [open, setOpen] = useState(false);
  const [blocks, setBlocks] = useState<number | null>(null);
  const [why, setWhy] = useState<TraceItem[]>([]);
  const [impact, setImpact] = useState<TraceItem[]>([]);
  const rootRef = useRef<HTMLSpanElement>(null);

  const toggle = async () => {
    if (open) {
      setOpen(false);
      return;
    }
    setOpen(true);
    const [c, t] = await Promise.all([
      get<{ counts: Record<string, number> }>(
        `/projects/${projectId}/initiatives/${initiativeId}/impact-counts?nodes=${nodeType}:${nodeId}`,
      ).catch(() => null),
      get<{ why: TraceItem[]; impact: TraceItem[] }>(
        `/projects/${projectId}/initiatives/${initiativeId}/trace?type=${nodeType}&id=${nodeId}`,
      ).catch(() => null),
    ]);
    setBlocks(c?.counts?.[`${nodeType}:${nodeId}`] ?? 0);
    setWhy(t?.why ?? []);
    setImpact(t?.impact ?? []);
  };

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("keydown", onKey);
    document.addEventListener("mousedown", onDown);
    return () => {
      document.removeEventListener("keydown", onKey);
      document.removeEventListener("mousedown", onDown);
    };
  }, [open]);

  return (
    <span className="lineage-chip" ref={rootRef}>
      <button
        type="button"
        className={`idchip ${className}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={toggle}
      >
        {label}
      </button>
      {open && (
        <div className="lineage-pop" role="dialog" aria-label={`Lineage of ${label}`}>
          <div className="lineage-pop-head">
            <span className="lineage-pop-title">{label}</span>
            {blocks !== null && blocks > 0 && <span className="lineage-blocks">blocks {blocks}</span>}
          </div>
          {why.length > 0 && (
            <>
              <div className="lineage-pop-sub">Depends on</div>
              <ul className="lineage-pop-list">
                {why.slice(0, 12).map((it, i) => (
                  <li key={`w${i}`} className="lineage-pop-row">
                    <span className="lineage-edge">{it.edge_type}</span>
                    <span className="lineage-node">
                      {it.to_label || `${it.to_type} ${it.to_id.slice(0, 8)}`}
                    </span>
                  </li>
                ))}
              </ul>
            </>
          )}
          {impact.length > 0 && (
            <>
              <div className="lineage-pop-sub">Blocks</div>
              <ul className="lineage-pop-list">
                {impact.slice(0, 12).map((it, i) => (
                  <li key={`i${i}`} className="lineage-pop-row">
                    <span className="lineage-edge">{it.edge_type}</span>
                    <span className="lineage-node">{nodeLabel(it)}</span>
                  </li>
                ))}
                {impact.length > 12 && <li className="lineage-pop-more">+{impact.length - 12} more</li>}
              </ul>
            </>
          )}
          {why.length === 0 && impact.length === 0 && (
            <div className="lineage-pop-empty">No lineage.</div>
          )}
        </div>
      )}
    </span>
  );
}
