// GraphNavLinks is the sidebar's "Product graph" group: Gaps, Outcomes and
// Quality, the initiative-wide views that belong to no single phase. Kept
// out of the phase rail so the rail still reads as the six phases in order.
export const GRAPH_VIEWS = [
  { view: "gaps", label: "Gaps" },
  { view: "outcomes", label: "Outcomes" },
  { view: "quality", label: "Quality" },
] as const;

export type GraphView = (typeof GRAPH_VIEWS)[number]["view"];

export default function GraphNavLinks({
  initiativeId,
  active,
  onNavigate,
}: {
  initiativeId: string;
  active?: string;
  onNavigate: (href: string) => void;
}) {
  return (
    <nav className="pn-graph" aria-label="Product graph">
      <div className="pn-eyebrow">Product graph</div>
      <ul className="pn-graph-list">
        {GRAPH_VIEWS.map((v) => (
          <li key={v.view}>
            <a
              className={`pn-graph-link ${active === v.view ? "on" : ""}`}
              href={`#/i/${initiativeId}/${v.view}`}
              aria-current={active === v.view ? "page" : undefined}
              onClick={(e) => {
                e.preventDefault();
                onNavigate(`#/i/${initiativeId}/${v.view}`);
              }}
            >
              {v.label}
            </a>
          </li>
        ))}
      </ul>
    </nav>
  );
}
