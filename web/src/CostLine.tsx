import { useState } from "react";
import { costLine, phaseBreakdownLines, type CostsView } from "./cost";

// CostLine is the compact spend-and-time line (C3: "Cost was invisible"),
// shown in PhaseNav under the initiative header. The phase breakdown shows
// on click, per the wave-3 contract.
export default function CostLine({ costs }: { costs: CostsView | null | undefined }) {
  const [open, setOpen] = useState(false);
  if (!costs) return null;
  const breakdown = phaseBreakdownLines(costs.by_phase ?? []);

  return (
    <div>
      <button className="w3-cost-line" onClick={() => setOpen((v) => !v)} title="Per-phase breakdown">
        {costLine(costs)}
      </button>
      {open && breakdown.length > 0 && (
        <ul className="w3-cost-breakdown">
          {breakdown.map((line) => (
            <li key={line}>{line}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
