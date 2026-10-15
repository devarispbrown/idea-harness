import { useState, type ReactNode } from "react";
import { lineageHref, nodeDisplay, nodeRef, nodeTypeLabel } from "./lineage";

// Small shared pieces for the Product Graph views (lineage, gaps, outcomes,
// quality, review convergence).

// CopyCommand shows a CLI command with a copy button -- the fallback wherever
// the UI cannot take the action itself. The status is announced politely so
// a screen-reader user hears that the copy happened.
export function CopyCommand({ command, label }: { command: string; label?: string }) {
  const [copied, setCopied] = useState<"" | "ok" | "fail">("");
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied("ok");
    } catch {
      // Clipboard access is refused on a non-secure origin or without a user
      // gesture; the command is still selectable on screen.
      setCopied("fail");
    }
    setTimeout(() => setCopied(""), 2000);
  };
  return (
    <span className="gv-cli">
      {label && <span className="gv-cli-label">{label}</span>}
      <code className="gv-cli-cmd">{command}</code>
      <button type="button" className="gv-copy" onClick={copy} aria-label={`Copy command: ${command}`}>
        {copied === "ok" ? "Copied" : "Copy"}
      </button>
      <span className="sr-only" role="status">
        {copied === "ok" ? "Copied to clipboard" : copied === "fail" ? "Copy failed; select the command instead" : ""}
      </span>
    </span>
  );
}

// NodeLink is one graph node rendered code-first and linked to its own
// lineage page.
export function NodeLink({
  initiativeId,
  type,
  id,
  label,
  showType = false,
}: {
  initiativeId: string;
  type: string;
  id: string;
  label?: string;
  showType?: boolean;
}) {
  const d = nodeDisplay(type, id, label);
  return (
    <a className="gv-node" href={lineageHref(initiativeId, type, nodeRef(type, id, label))} title={`Lineage of ${d.code}`}>
      <span className="gv-node-code">{d.code}</span>
      {d.text && <span className="gv-node-text">{d.text}</span>}
      {showType && d.text !== nodeTypeLabel(type) && <span className="gv-node-type">{type.replace(/_/g, " ")}</span>}
    </a>
  );
}

// Tone is the shared four-colour status scale (quality-* classes in
// styles.css).
export function ToneChip({ tone, children }: { tone: string; children: ReactNode }) {
  return <span className={`health-sev quality-${tone}`}>{children}</span>;
}
