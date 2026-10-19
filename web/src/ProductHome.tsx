import { useEffect, useRef, useState } from "react";
import { derivePhases, PHASE_LABELS, PHASE_PURPOSE, rollupPhaseSource } from "./phases";
import type { InitiativeRollup } from "./phases";
import { PHASES, type PhaseName } from "./router";
import { get, resource } from "./api";
import { displayTitle } from "./title";
import { isSubmitShortcut } from "./shortcuts";
import { entriesSince, getLastVisit } from "./lastVisit";
import { describeTimelineEntry, type TimelineEntry } from "./timeline";

interface Initiative {
  id: string;
  title: string;
  // Wave 3 (C1): see title.ts's displayTitle -- this was the ICP finding
  // that "Home titles were the whole 600-character prompt."
  short_title?: string;
  description: string;
  status: string;
}

// "shipped" asserted every complete initiative was a real deploy, the same
// overclaim buildLogic.ts's appliedOutcomeLabel exists to avoid on Build's
// own change-set pills (G3) -- a demo/placeholder-executor initiative that
// never touched anything real read exactly like one that did. ProductHome's
// rollup carries no per-change-set model/executor detail (that lives on
// BuildPage's own change-set list), so there is no cheap way to tell the two
// apart here; "Complete" claims only what's actually known at this level.
const STATUS_PILL: Record<string, { label: string; state: string }> = {
  running: { label: "running", state: "running" },
  needs_human_judgment: { label: "needs you", state: "needs-you" },
  approved: { label: "approved", state: "verified" },
  plan_approved: { label: "plan approved", state: "verified" },
  complete: { label: "Complete", state: "verified" },
  failed: { label: "failed", state: "failing" },
};

// An attached grounding doc: the file itself plus a stable id for the file
// chip's React key (file.name can repeat, so the name alone can't key it).
interface AttachedFile {
  id: string;
  file: File;
}

// isImageFile decides the per-file badge ("text" / "image"). The daemon
// infers each doc's kind from its extension; this only needs to distinguish
// the two badge words, so it checks the MIME type and falls back to common
// image extensions when a browser reports an empty/octet-stream type.
const IMAGE_EXT = /\.(jpe?g|png|gif|webp|svg|bmp|avif|heic|heif)$/i;
function isImageFile(file: File): boolean {
  return file.type.startsWith("image/") || IMAGE_EXT.test(file.name);
}

export default function ProductHome({
  projectId,
  initiatives,
  attention,
  projectName,
  onOpen,
  onCreate,
  onOpenSettings,
}: {
  projectId: string;
  initiatives: Initiative[];
  attention: InitiativeRollup[];
  projectName: string;
  onOpen: (id: string) => void;
  onCreate: (brief: string, files: File[], from?: string) => void;
  onOpenSettings: () => void;
}) {
  const [brief, setBrief] = useState("");
  // Files attached to the compose before Start. Held locally so the person
  // can add/remove grounding docs before the initiative exists on the server.
  const [files, setFiles] = useState<AttachedFile[]>([]);
  // Seed-from (brownfield): the initiative whose live requirements this new
  // initiative revises, mirroring `idea new --from`.
  const [from, setFrom] = useState("");
  const [dragging, setDragging] = useState(false);
  // Shown once, after an empty Start, until the brief has content again --
  // clicking Start with nothing typed used to just do nothing (K).
  const [ideaHint, setIdeaHint] = useState(false);
  const briefRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const nextFileId = useRef(0);
  const rollupById = new Map(attention.map((r) => [r.id, r]));

  const needYou = initiatives.filter((i) => i.status === "needs_human_judgment").length;

  // "Since you were last here" (C1 / JTBD 6). For every initiative that has
  // a recorded last-visit cursor (lastVisit.ts, localStorage), fetch its
  // timeline since that cursor -- App.tsx advances the cursor to "now" the
  // moment that initiative is actually opened, so this always reads the
  // PREVIOUS visit's cutoff, never the one about to be set. A first-ever
  // visit has no cursor and is skipped: there is nothing to compare against,
  // and showing "everything" would read as a huge, undifferentiated backlog
  // rather than "what's new".
  const [sinceLastVisit, setSinceLastVisit] = useState<Record<string, TimelineEntry[]>>({});
  const [expandedSince, setExpandedSince] = useState<Record<string, boolean>>({});
  const initiativeIds = initiatives.map((i) => i.id).join(",");
  useEffect(() => {
    if (!projectId || !initiatives.length) return;
    let cancelled = false;
    (async () => {
      const entries: [string, TimelineEntry[]][] = await Promise.all(
        initiatives.map(async (i): Promise<[string, TimelineEntry[]]> => {
          const since = getLastVisit(i.id);
          if (!since) return [i.id, []];
          try {
            const list = await get<TimelineEntry[]>(
              `${resource(projectId, i.id, "timeline")}?since=${encodeURIComponent(since)}`,
            );
            // Defensive re-filter (entriesSince): the backend's `?since=` is
            // already the source of truth, but this is the same pure
            // boundary check unit-tested in lastVisit.test.ts, applied here
            // too rather than trusting an unfamiliar query param blindly.
            return [i.id, entriesSince(list ?? [], since)];
          } catch {
            return [i.id, []];
          }
        }),
      );
      if (cancelled) return;
      const map: Record<string, TimelineEntry[]> = {};
      for (const [id, list] of entries) if (list.length > 0) map[id] = list;
      setSinceLastVisit(map);
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- initiativeIds is the stable key for `initiatives`' identity.
  }, [projectId, initiativeIds]);

  // addFiles appends one or more picked/dropped/pasted files. The hidden
  // <input type=file> and the drop-zone both funnel here, so there is one
  // place that dedupes the React key and clears the empty-submit hint.
  const addFiles = (incoming: File[]) => {
    if (incoming.length === 0) return;
    setFiles((prev) => [
      ...prev,
      ...incoming.map((file) => ({ id: `f${nextFileId.current++}`, file })),
    ]);
    if (ideaHint) setIdeaHint(false);
  };
  const removeFile = (id: string) => setFiles((prev) => prev.filter((f) => f.id !== id));
  // onPaste: a clipboard image/file becomes an attachment; pasted text falls
  // through to the textarea's normal paste behavior.
  const onPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const pasted = e.clipboardData?.files;
    if (pasted && pasted.length > 0) {
      e.preventDefault();
      addFiles(Array.from(pasted));
    }
  };

  const submitIdea = () => {
    if (brief.trim()) onCreate(brief.trim(), files.map((f) => f.file), from || undefined);
    else {
      setIdeaHint(true);
      briefRef.current?.focus();
    }
  };
  // The header's "+ New idea" button just brings the compose into view (and
  // keyboard focus); it no longer submits, since the compose is now a full
  // brief + attachments surface rather than a one-line field.
  const focusCompose = () => briefRef.current?.focus();

  return (
    <div className="product-home">
      <header className="ph-topbar">
        <span className="pn-logo-mark">ih</span>
        {/* The literal string "my-product" used to show here regardless of
            which project was open. */}
        <span className="ph-project">{projectName || "Project"}</span>
        <button className="ph-settings" onClick={onOpenSettings}>
          Settings
        </button>
        <button className="ph-new" onClick={focusCompose}>+ New idea</button>
      </header>
      {/* A single <main> landmark per page -- App.tsx already renders one
          around this component, so this is a plain div (K). */}
      <div className="ph-main">
        <div className="ph-head">
          <h1 className="ph-title">Initiatives</h1>
          <span className="muted">{initiatives.length} total</span>
          {needYou > 0 && <span className="ph-need">{needYou} need you</span>}
        </div>
        {/* The "New initiative" compose: a brief + optional grounding-doc
            attachments. It replaced the single-line idea input; title and
            description are derived from the brief by App.tsx (title = first
            non-empty line, description = full trimmed brief). */}
        <div className="ph-create">
          <label className="ph-compose-label" htmlFor="ph-brief">
            Brief
          </label>
          <textarea
            id="ph-brief"
            ref={briefRef}
            className="text-input ph-brief"
            placeholder="What are you thinking about building?"
            value={brief}
            rows={3}
            onChange={(e) => {
              setBrief(e.target.value);
              if (ideaHint) setIdeaHint(false);
            }}
            onPaste={onPaste}
            onKeyDown={(e) => {
              if (isSubmitShortcut(e.nativeEvent)) {
                e.preventDefault();
                submitIdea();
              }
            }}
          />
          <div
            className={`ph-dropzone ${dragging ? "dragging" : ""}`}
            role="button"
            tabIndex={0}
            aria-label="Attach a grounding document (click to browse, drag and drop, or paste)"
            onClick={() => fileInputRef.current?.click()}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                fileInputRef.current?.click();
              }
            }}
            onDragOver={(e) => {
              e.preventDefault();
              setDragging(true);
            }}
            onDragLeave={() => setDragging(false)}
            onDrop={(e) => {
              e.preventDefault();
              setDragging(false);
              addFiles(Array.from(e.dataTransfer.files ?? []));
            }}
          >
            <div className="ph-dropzone-main">
              Drop files here, <u>browse</u>, or paste
            </div>
            <div className="ph-dropzone-hint">
              .md .txt .pdf .docx .xlsx .jpg .png … · each file becomes a grounding doc
            </div>
          </div>
          <input
            ref={fileInputRef}
            type="file"
            multiple
            accept=".md,.txt,.pdf,.docx,.xlsx,.jpg,.jpeg,.png,.gif,.webp,.svg"
            style={{ display: "none" }}
            onChange={(e) => {
              addFiles(Array.from(e.target.files ?? []));
              e.target.value = "";
            }}
          />
          {files.length > 0 && (
            <ul className="ph-files" aria-label="Attached files">
              {files.map(({ id, file }) => (
                <li key={id} className="ph-file">
                  <span className={`ph-file-badge ${isImageFile(file) ? "image" : "text"}`}>
                    {isImageFile(file) ? "image" : "text"}
                  </span>
                  <span className="ph-file-name" title={file.name}>
                    {file.name}
                  </span>
                  <button
                    type="button"
                    className="ph-file-remove"
                    aria-label={`Remove ${file.name}`}
                    onClick={() => removeFile(id)}
                  >
                    ✕
                  </button>
                </li>
              ))}
            </ul>
          )}
          {initiatives.length > 0 && (
            <label className="ph-from">
              <span className="ph-from-label">Seed from an existing initiative (optional)</span>
              <select
                className="text-input"
                value={from}
                onChange={(e) => setFrom(e.target.value)}
              >
                <option value="">Start fresh</option>
                {initiatives.map((i) => (
                  <option key={i.id} value={i.id}>
                    {displayTitle(i) || i.title}
                  </option>
                ))}
              </select>
            </label>
          )}
          <div className="ph-compose-actions">
            <button type="button" className="ph-start" onClick={submitIdea}>
              Start
            </button>
          </div>
          {ideaHint && (
            <p className="ph-create-hint">
              Write a line or two about what you're building, attach any grounding docs if you have
              them, then press Start — e.g. "Add CSV export to the reports page".
            </p>
          )}
        </div>
        {initiatives.length === 0 ? (
          <ProductHomeExplainer />
        ) : (
          <div className="ph-list">
            {initiatives.map((i, idx) => {
              const pill = STATUS_PILL[i.status];
              const rollup = rollupById.get(i.id);
              const phases: { phase: PhaseName; state: string }[] = rollup
                ? derivePhases(rollupPhaseSource(rollup)).map((s) => ({ phase: s.phase, state: s.state }))
                : [];
              const since = sinceLastVisit[i.id] ?? [];
              const isExpanded = !!expandedSince[i.id];
              return (
                <section key={i.id} className="ph-card" onClick={() => onOpen(i.id)}>
                  <div className="ph-card-head">
                    <span className="ph-num">#{idx + 1}</span>
                    {/* C1 / JTBD 6: "Home titles were the whole 600-character
                        prompt." The full idea is still available as the
                        card's description; this is just the title shown. */}
                    {/* The card is clickable anywhere with a mouse, but a
                        <section onClick> is not a Tab stop: the title is the
                        real control, so the keyboard and a screen reader
                        reach every initiative too. */}
                    <button
                      type="button"
                      className="ph-card-title ph-card-open"
                      onClick={(e) => {
                        e.stopPropagation();
                        onOpen(i.id);
                      }}
                    >
                      {displayTitle(i)}
                    </button>
                    {pill && <span className={`phase-pill pill-${pill.state}`}>{pill.label}</span>}
                  </div>
                  <div className="ph-bars">
                    {phases.map((p) => (
                      <div key={p.phase} className="ph-bar">
                        <span className={`ph-bar-fill ${p.state}`} />
                        <span className={`ph-bar-label ${p.state}`}>{PHASE_LABELS[p.phase]}</span>
                      </div>
                    ))}
                  </div>
                  {since.length > 0 && (
                    <button
                      className="w3-since-last"
                      onClick={(e) => {
                        e.stopPropagation();
                        setExpandedSince((m) => ({ ...m, [i.id]: !m[i.id] }));
                      }}
                    >
                      {since.length} update{since.length === 1 ? "" : "s"} since you were last here
                      {isExpanded ? " ▾" : " ▸"}
                    </button>
                  )}
                  {since.length > 0 && isExpanded && (
                    <ul className="w3-timeline-list" onClick={(e) => e.stopPropagation()}>
                      {since.map((entry, ei) => (
                        <li key={ei} className="w3-timeline-row">
                          <span>{describeTimelineEntry(entry)}</span>
                        </li>
                      ))}
                    </ul>
                  )}
                </section>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}

// ProductHomeExplainer is the first-run empty state (G4): before this, an
// empty project showed nothing but "No initiatives yet — start one above.",
// with no explanation of what the six phases mean or what happens next.
function ProductHomeExplainer() {
  return (
    <div className="ph-empty">
      <p className="ph-empty-lead">
        Every idea moves through the same six phases, in order, from a rough idea to something
        verified against its own requirements:
      </p>
      <ol className="ph-empty-phases">
        {PHASES.map((phase) => (
          <li key={phase} className="ph-empty-phase">
            <span className="ph-empty-phase-name">{PHASE_LABELS[phase]}</span>
            <span className="ph-empty-phase-purpose">{PHASE_PURPOSE[phase]}</span>
          </li>
        ))}
      </ol>
      <p className="ph-empty-next">
        <strong>Next:</strong> write what you're thinking about building above, then press Start.
      </p>
    </div>
  );
}
