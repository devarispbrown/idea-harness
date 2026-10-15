import { stripSourceTagsInText } from "./verify";
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
      // One level down: the page's h1 is the document's title (docTitle),
      // so a document's own # headings must not be h1s too.
      const level = trimmed.match(/^#+/)![0].length;
      const text = trimmed.replace(/^#+\s*/, "");
      if (level === 1) out.push(<h2 key={i}>{inline(text)}</h2>);
      else if (level === 2) out.push(<h3 key={i}>{inline(text)}</h3>);
      else out.push(<h4 key={i}>{inline(text)}</h4>);
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

// docTitle splits a document into the page's title -- its first line when
// that is a "# " heading -- and the rest. The title becomes the page's one
// h1; a document without one is titled by its kind.
export function docTitle(text: string): { title: string; rest: string } {
  const lines = text.split("\n");
  const first = lines.findIndex((l) => l.trim() !== "");
  if (first >= 0 && /^#\s/.test(lines[first].trim())) {
    return { title: lines[first].trim().replace(/^#\s*/, ""), rest: lines.slice(first + 1).join("\n") };
  }
  return { title: "", rest: text };
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
  const [notFound, setNotFound] = useState(false);
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
    setNotFound(false);
    get<{ kind: string; body: string }>(`/projects/${projectId}/artifacts/${artifactId}/body`)
      .then((v) => {
        setBody(v?.body ?? "");
        setKind(v?.kind ?? "");
      })
      // A failed fetch (bogus artifact id) is not the same thing as a real
      // document with an empty body -- it used to render the same "No
      // content." either way (K).
      .catch(() => setNotFound(true));
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

  const doc = docTitle(stripSourceTagsInText(body));

  return (
    <div className="phase-page">
      <div className="doc-reader-head">
        <button className="ghost" onClick={() => navigate(`#/i/${initiativeId}/explore`)}>
          ← Back
        </button>
        {kind && <span className="code">{kind}</span>}
      </div>
      <article className="doc-reader-body" aria-labelledby="doc-reader-title">
        <h1 id="doc-reader-title" className="doc-reader-title">
          {notFound ? "Document not found" : doc.title ? inline(doc.title) : kind || "Document"}
        </h1>
        {notFound ? null : doc.rest.trim() ? renderMarkdown(doc.rest) : <p className="muted">No content.</p>}
      </article>

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
