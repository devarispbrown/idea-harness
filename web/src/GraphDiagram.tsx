import { useEffect, useState } from "react";
import { get, getOutcomes, resource } from "./api";

interface Counted {
  status?: string;
}

export interface GraphLayer {
  key: string;
  label: string;
  count: number;
  // How many of `count` are in a "done" state for that layer (verified,
  // applied, on track); undefined where the layer has no such notion.
  done?: number;
  doneLabel?: string;
  href: string;
}

// graphLayers is the initiative's Product Graph collapsed to one box per
// layer, in the chain's order objective → requirement → task → change set →
// evidence. Pure, so the counting is testable.
export function graphLayers(
  initiativeId: string,
  d: { objectives: number; onTrack: number; reqs: Counted[]; tasks: Counted[]; changeSets: Counted[]; evidence: number },
): GraphLayer[] {
  const base = `#/i/${initiativeId}`;
  const live = d.reqs.filter((r) => r.status !== "removed" && r.status !== "superseded");
  return [
    { key: "objective", label: "Objectives", count: d.objectives, done: d.onTrack, doneLabel: "metrics on track", href: `${base}/outcomes` },
    { key: "requirement", label: "Requirements", count: live.length, done: live.filter((r) => r.status === "verified").length, doneLabel: "verified", href: `${base}/define` },
    { key: "task", label: "Tasks", count: d.tasks.length, done: d.tasks.filter((t) => t.status === "done").length, doneLabel: "done", href: `${base}/plan` },
    { key: "change_set", label: "Change sets", count: d.changeSets.length, done: d.changeSets.filter((c) => c.status === "applied").length, doneLabel: "applied", href: `${base}/build` },
    { key: "evidence", label: "Quality evidence", count: d.evidence, href: `${base}/quality` },
  ];
}

// GraphDiagram is the Health page's mini-diagram of the chain: five linked
// boxes in inline SVG (no chart library), each opening the page that holds
// that layer. A text list carries the same numbers for assistive tech.
export default function GraphDiagram({ projectId, initiativeId }: { projectId: string; initiativeId: string }) {
  const [layers, setLayers] = useState<GraphLayer[] | null>(null);

  useEffect(() => {
    let live = true;
    Promise.all([
      getOutcomes(projectId, initiativeId).catch(() => null),
      get<Counted[]>(resource(projectId, initiativeId, "requirements")).catch(() => []),
      get<Counted[]>(resource(projectId, initiativeId, "tasks")).catch(() => []),
      get<Counted[]>(resource(projectId, initiativeId, "change-sets")).catch(() => []),
      get<unknown[]>(resource(projectId, initiativeId, "quality/history")).catch(() => []),
    ]).then(([o, reqs, tasks, cs, q]) => {
      if (!live) return;
      setLayers(
        graphLayers(initiativeId, {
          objectives: o?.summary.objectives ?? 0,
          onTrack: o?.summary.on_track ?? 0,
          reqs: reqs ?? [],
          tasks: tasks ?? [],
          changeSets: cs ?? [],
          evidence: (q ?? []).length,
        }),
      );
    });
    return () => {
      live = false;
    };
  }, [projectId, initiativeId]);

  if (!layers) return null;
  const W = 150;
  const GAP = 34;
  const H = 74;
  const total = layers.length * W + (layers.length - 1) * GAP;
  return (
    <figure className="gd">
      <svg className="gd-svg" viewBox={`0 0 ${total} ${H}`} role="group" aria-labelledby="gd-cap">
        <defs>
          <marker id="gd-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto">
            <path d="M0,0 L8,4 L0,8 z" className="gd-arrowhead" />
          </marker>
        </defs>
        {layers.map((l, i) => {
          const x = i * (W + GAP);
          return (
            <g key={l.key}>
              {i > 0 && <line className="gd-edge" x1={x - GAP + 2} y1={H / 2} x2={x - 3} y2={H / 2} markerEnd="url(#gd-arrow)" />}
              <a href={l.href} aria-label={`${l.label}: ${l.count}`}>
                <rect className={`gd-box ${l.count === 0 ? "gd-empty" : ""}`} x={x + 1} y={1} width={W - 2} height={H - 2} rx={10} />
                <text className="gd-count" x={x + W / 2} y={30} textAnchor="middle">
                  {l.count}
                </text>
                <text className="gd-label" x={x + W / 2} y={48} textAnchor="middle">
                  {l.label}
                </text>
                {l.done !== undefined && l.count > 0 && (
                  <text className="gd-done" x={x + W / 2} y={63} textAnchor="middle">
                    {l.done} {l.doneLabel}
                  </text>
                )}
              </a>
            </g>
          );
        })}
      </svg>
      <figcaption id="gd-cap" className="sr-only">
        Product graph:{" "}
        {layers.map((l) => `${l.count} ${l.label.toLowerCase()}${l.done !== undefined ? ` (${l.done} ${l.doneLabel})` : ""}`).join(", then ")}
      </figcaption>
    </figure>
  );
}
