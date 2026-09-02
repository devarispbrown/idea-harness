import { useEffect, useState, type ReactNode } from "react";
import { get } from "./api";
import { navigate } from "./router";

// Minimal safe markdown renderer (headings, bold, lists, paragraphs). Builds
// React nodes, never raw HTML, so sanitized markdown stays inert.
function renderMarkdown(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  const lines = text.split("\n");
  let list: string[] = [];
  const flushList = (key: string) => {
    if (list.length) {
      out.push(
        <ul key={key}>
          {list.map((li, i) => <li key={i}>{inline(li)}</li>)}
        </ul>,
      );
      list = [];
    }
  };
  lines.forEach((line, i) => {
    const trimmed = line.trim();
    if (trimmed === "") {
      flushList(`l${i}`);
      return;
    }
    if (/^#{1,3}\s/.test(trimmed)) {
      flushList(`l${i}`);
      const level = trimmed.match(/^#+/)![0].length;
      const text = trimmed.replace(/^#+\s*/, "");
      if (level === 1) out.push(<h1 key={i}>{inline(text)}</h1>);
      else if (level === 2) out.push(<h2 key={i}>{inline(text)}</h2>);
      else out.push(<h3 key={i}>{inline(text)}</h3>);
      return;
    }
    if (/^[-*]\s/.test(trimmed)) {
      list.push(trimmed.replace(/^[-*]\s*/, ""));
      return;
    }
    flushList(`l${i}`);
    out.push(<p key={i}>{inline(trimmed)}</p>);
  });
  flushList("lend");
  return out;
}

function inline(text: string): ReactNode[] {
  const parts = text.split(/(\*\*[^*]+\*\*)/g);
  return parts.map((p, i) =>
    p.startsWith("**") && p.endsWith("**") ? <strong key={i}>{p.slice(2, -2)}</strong> : p,
  );
}

export default function DocReader({
  projectId,
  initiativeId,
  artifactId,
}: {
  projectId: string;
  initiativeId: string;
  artifactId: string;
}) {
  const [body, setBody] = useState("");
  const [kind, setKind] = useState("");

  useEffect(() => {
    get<{ kind: string; body: string }>(`/projects/${projectId}/artifacts/${artifactId}/body`)
      .then((v) => {
        setBody(v?.body ?? "");
        setKind(v?.kind ?? "");
      })
      .catch(() => setBody(""));
  }, [projectId, artifactId]);

  return (
    <div className="phase-page">
      <div className="doc-reader-head">
        <button className="ghost" onClick={() => navigate(`#/i/${initiativeId}/explore`)}>
          ← Back
        </button>
        {kind && <span className="code">{kind}</span>}
      </div>
      <article className="doc-reader-body">{body ? renderMarkdown(body) : "No content."}</article>
    </div>
  );
}
