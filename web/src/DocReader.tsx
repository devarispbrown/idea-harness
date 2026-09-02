import { useCallback, useEffect, useState, type ReactNode } from "react";
import { get, post } from "./api";
import { navigate } from "./router";

interface DocComment {
  id: string;
  section?: string;
  text: string;
  author: string;
  promoted_question_id?: string;
}

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
  const [comments, setComments] = useState<DocComment[]>([]);
  const [section, setSection] = useState("");
  const [text, setText] = useState("");

  const loadComments = useCallback(async () => {
    const c = await get<DocComment[]>(
      `/projects/${projectId}/artifacts/${artifactId}/comments`,
    ).catch(() => []);
    setComments(c ?? []);
  }, [projectId, artifactId]);

  useEffect(() => {
    get<{ kind: string; body: string }>(`/projects/${projectId}/artifacts/${artifactId}/body`)
      .then((v) => {
        setBody(v?.body ?? "");
        setKind(v?.kind ?? "");
      })
      .catch(() => setBody(""));
    loadComments();
  }, [projectId, artifactId, loadComments]);

  const addComment = async () => {
    if (!text.trim()) return;
    try {
      await post(`/projects/${projectId}/artifacts/${artifactId}/comments`, {
        section: section.trim(),
        text: text.trim(),
      });
      setText("");
      setSection("");
      await loadComments();
    } catch {
      /* surfaced by the empty list; no silent state */
    }
  };

  const promote = async (id: string) => {
    try {
      await post(`/projects/${projectId}/comments/${id}/promote`, {});
      await loadComments();
    } catch {
      /* ignore */
    }
  };

  return (
    <div className="phase-page">
      <div className="doc-reader-head">
        <button className="ghost" onClick={() => navigate(`#/i/${initiativeId}/explore`)}>
          ← Back
        </button>
        {kind && <span className="code">{kind}</span>}
      </div>
      <article className="doc-reader-body">{body ? renderMarkdown(body) : "No content."}</article>

      <section className="doc-comments">
        <div className="doc-eyebrow">Comments</div>
        <div className="doc-comment-form">
          <input
            className="text-input"
            placeholder="Section (optional)"
            value={section}
            onChange={(e) => setSection(e.target.value)}
          />
          <input
            className="text-input"
            placeholder="Leave a comment…"
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && text.trim()) addComment();
            }}
          />
          <button className="cs-secondary" onClick={addComment}>
            Add
          </button>
        </div>
        {comments.map((c) => (
          <div key={c.id} className="doc-comment">
            <div className="doc-comment-line">
              {c.section && <span className="doc-comment-section">{c.section}</span>}
              <span className="doc-comment-text">{c.text}</span>
            </div>
            {c.promoted_question_id ? (
              <span className="doc-comment-promoted">promoted to a question</span>
            ) : (
              <button className="jc-ghost" onClick={() => promote(c.id)}>
                Promote to question
              </button>
            )}
          </div>
        ))}
        {comments.length === 0 && <p className="muted">No comments yet.</p>}
      </section>
    </div>
  );
}
