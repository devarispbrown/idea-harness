import { useCallback, useEffect, useRef, useState } from "react";
import { apiBlob, get, post, resource, uploadDoc } from "./api";
import PhaseHeader from "./PhaseHeader";
import type { StatusPill } from "./PhaseHeader";
import { lineageHref, navigate } from "./router";

interface ContextDoc {
  id: string;
  kind: string;
  title: string;
  digest: string;
  preview: string;
  format?: string;
  description_digest?: string;
}
interface Artifact {
  id: string;
  kind: string;
  version: number;
}

const KIND_LABEL: Record<string, string> = {
  prd: "prd",
  design: "design",
  tech: "tech",
  market: "market",
  other: "other",
};
// Colours come from CSS custom properties, not literal hex, so an emulated
// dark color scheme (which redefines these tokens in styles.css) actually
// changes what renders here instead of leaving a light-mode chip stuck on a
// dark background (L).
const KIND_CHIP: Record<string, { bg: string; fg: string }> = {
  prd: { bg: "var(--kind-prd-bg)", fg: "var(--kind-prd-fg)" },
  design: { bg: "var(--kind-design-bg)", fg: "var(--kind-design-fg)" },
  tech: { bg: "var(--kind-tech-bg)", fg: "var(--kind-tech-fg)" },
  market: { bg: "var(--kind-market-bg)", fg: "var(--kind-market-fg)" },
  other: { bg: "var(--kind-other-bg)", fg: "var(--kind-other-fg)" },
};

export default function ExplorePage({
  projectId,
  initiativeId,
  status,
}: {
  projectId: string;
  initiativeId: string;
  status: string;
}) {
  const [docs, setDocs] = useState<ContextDoc[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [kind, setKind] = useState("other");
  const [pasteOpen, setPasteOpen] = useState(false);
  const [pasteText, setPasteText] = useState("");
  const [reject, setReject] = useState<{ message: string; name?: string } | null>(null);
  const [dragging, setDragging] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const load = useCallback(async () => {
    const [d, a] = await Promise.all([
      get<ContextDoc[]>(`/projects/${projectId}/initiatives/${initiativeId}/docs`).catch(() => []),
      get<Artifact[]>(resource(projectId, initiativeId, "artifacts")).catch(() => []),
    ]);
    setDocs(d ?? []);
    setArtifacts(a ?? []);
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
    const id = setInterval(load, 2500);
    return () => clearInterval(id);
  }, [load]);

  const addFile = async (file: File) => {
    setReject(null);
    try {
      await uploadDoc(`/projects/${projectId}/initiatives/${initiativeId}/docs`, file, { kind });
      await load();
    } catch (e) {
      setReject({ message: e instanceof Error ? e.message : String(e), name: file.name });
    }
  };
  const addPaste = async () => {
    setReject(null);
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/docs`, {
        content: pasteText,
        kind,
      });
      setPasteText("");
      setPasteOpen(false);
      await load();
    } catch (e) {
      setReject({ message: e instanceof Error ? e.message : String(e) });
    }
  };
  const download = async (doc: ContextDoc) => {
    try {
      const blob = await apiBlob(`/projects/${projectId}/initiatives/${initiativeId}/docs/${doc.id}`);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = doc.title || doc.kind;
      a.click();
      URL.revokeObjectURL(url);
    } catch {
      /* ignore */
    }
  };

  const produced = artifacts.filter((a) => a.kind === "idea_brief" || a.kind === "research_brief");
  const briefTitle: Record<string, string> = { idea_brief: "Idea brief", research_brief: "Research brief" };

  const pill: StatusPill | undefined =
    status === "running"
      ? { label: "running", state: "running" }
      : status === "needs_human_judgment" || status === "approved" || status === "plan_approved" || status === "complete"
        ? { label: "done", state: "verified" }
        : status === "failed"
          ? { label: "failing", state: "failing" }
          : undefined;

  return (
    <div className="phase-page">
      <PhaseHeader title="Explore" purpose="Framing the idea, then researching what's known." pill={pill} />

      <div className="doc-eyebrow" style={{ marginTop: 22 }}>Grounding</div>
      <p className="grounding-desc">
        Your own material, injected into design, planning, and PRD revisions. Text and documents
        are credential-swept and redacted on ingest, and never fed to a model or served back
        unredacted. Images are described by a vision model instead — the description, not the
        pixels, is what reaches the design agents.
      </p>
      <section className="grounding-card">
        {docs.map((d) => {
          if (d.format === "image" && !d.description_digest) {
            return (
              <div key={d.id} className="grounding-row describing">
                <span className="grounding-spinner" aria-hidden="true" />
                <div className="grounding-body">
                  <div className="grounding-title">{d.title}</div>
                  <div className="grounding-describing">Describing the image with a vision model — the description is what reaches the design agents.</div>
                </div>
              </div>
            );
          }
          return (
            <div key={d.id} className="grounding-row">
              <span
                className="grounding-kind"
                style={{
                  background: KIND_CHIP[d.kind]?.bg ?? KIND_CHIP.other.bg,
                  color: KIND_CHIP[d.kind]?.fg ?? KIND_CHIP.other.fg,
                }}
              >
                {KIND_LABEL[d.kind] ?? d.kind}
              </span>
              <div className="grounding-body">
                <div className="grounding-title">{d.title}</div>
                {d.preview && <div className="grounding-meta">{d.preview}</div>}
              </div>
              <button
                className="grounding-trace"
                aria-label={`Trace ${d.title}`}
                onClick={() => navigate(lineageHref(initiativeId, "context_doc", d.id))}
              >
                trace
              </button>
              <button className="grounding-download" onClick={() => download(d)}>
                Download
              </button>
            </div>
          );
        })}

        <div className="grounding-upload">
          <div
            className={`dropzone ${dragging ? "dragging" : ""}`}
            onClick={() => fileRef.current?.click()}
            onDragOver={(e) => {
              e.preventDefault();
              setDragging(true);
            }}
            onDragLeave={() => setDragging(false)}
            onDrop={(e) => {
              e.preventDefault();
              setDragging(false);
              const f = e.dataTransfer.files?.[0];
              if (f) addFile(f);
            }}
          >
            <div className="dropzone-main">Drop a file, or <u>browse</u></div>
            <div className="dropzone-hint">.md .txt .pdf .docx .xlsx .jpg .png · max 2 MiB · {docs.length} of 100 used</div>
          </div>
          <input
            ref={fileRef}
            type="file"
            accept=".md,.txt,.pdf,.docx,.xlsx,.jpg,.jpeg,.png,.gif,.webp"
            style={{ display: "none" }}
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) addFile(f);
              e.target.value = "";
            }}
          />
          <span className="grounding-or">or</span>
          <button className="grounding-paste" onClick={() => setPasteOpen((v) => !v)}>
            Paste text…
          </button>
        </div>

        {pasteOpen && (
          <div className="grounding-paste-open">
            <select className="text-input" value={kind} onChange={(e) => setKind(e.target.value)}>
              {["prd", "design", "tech", "market", "other"].map((k) => (
                <option key={k} value={k}>{k}</option>
              ))}
            </select>
            <textarea
              className="text-input"
              placeholder="Paste Markdown or plain text…"
              value={pasteText}
              onChange={(e) => setPasteText(e.target.value)}
              rows={4}
            />
            {pasteText.trim() && (
              <button className="jc-primary" onClick={addPaste}>
                Add
              </button>
            )}
          </div>
        )}

        {reject && (
          <div className="grounding-reject">
            <span className="grounding-reject-x">✕</span>
            <div className="grounding-reject-body">
              <div className="grounding-reject-title">
                {reject.message.includes("credential")
                  ? `${reject.name ? reject.name : "the upload"} was rejected — it contains a credential`
                  : "Upload failed"}
              </div>
              <div className="grounding-reject-sub">
                {reject.message.includes("credential")
                  ? "Nothing was stored and the contents are not echoed back. Remove the secret and upload again."
                  : reject.message}
              </div>
            </div>
            <button className="grounding-reject-dismiss" onClick={() => setReject(null)}>
              Dismiss
            </button>
          </div>
        )}
      </section>

      <div className="doc-eyebrow" style={{ marginTop: 24 }}>Produced</div>
      <div className="doc-list">
        {produced.map((a) => (
          <section key={a.id} className="doc-row">
            <div className="doc-body">
              <div className="doc-title">{briefTitle[a.kind] ?? a.kind} · v{a.version}</div>
            </div>
            <button
              className="doc-diff"
              onClick={async () => {
                const blob = await apiBlob(`/projects/${projectId}/artifacts/${a.id}/raw`);
                const url = URL.createObjectURL(blob);
                const el = document.createElement("a");
                el.href = url;
                el.download = `${a.kind}.md`;
                el.click();
                URL.revokeObjectURL(url);
              }}
            >
              Export .md
            </button>
            <button className="doc-read dark" onClick={() => navigate(`#/i/${initiativeId}/doc/${a.id}/${a.version}`)}>
              Read
            </button>
          </section>
        ))}
      </div>
    </div>
  );
}
