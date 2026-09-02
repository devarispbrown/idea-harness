import { useCallback, useEffect, useState } from "react";
import { get, post, apiRaw } from "./api";
import PhaseHeader from "./PhaseHeader";

interface Task {
  id: string;
  code: string;
  title: string;
  status: string;
}
interface ChangeSet {
  id: string;
  human_id: string;
  task_id: string;
  status: string;
  patch_digest: string;
}
interface Finding {
  id: string;
  severity: string;
  category: string;
  finding: string;
  recommendation: string;
  evidence?: string;
  disposition?: string;
  code?: string;
  target_type: string;
  target_id: string;
}
interface DiffFile {
  path: string;
  change_type: string;
  digest?: string;
  size?: number;
  content?: string;
}

const DIFF_BADGE: Record<string, string> = {
  add: "added",
  modify: "modified",
  delete: "deleted",
};

const STATUS_PILL: Record<string, { label: string; state: string }> = {
  draft: { label: "draft", state: "inactive" },
  checks_running: { label: "checking", state: "running" },
  review_required: { label: "review required", state: "needs-you" },
  rejected: { label: "rejected", state: "failing" },
  changes_requested: { label: "changes requested", state: "needs-you" },
  superseded: { label: "superseded", state: "inactive" },
  approved: { label: "approved", state: "verified" },
  applying: { label: "applying", state: "running" },
  applied: { label: "applied", state: "verified" },
  reverted: { label: "reverted", state: "inactive" },
  conflicted: { label: "conflicted", state: "needs-you" },
};

export default function BuildPage({
  projectId,
  initiativeId,
}: {
  projectId: string;
  initiativeId: string;
}) {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [changeSets, setChangeSets] = useState<ChangeSet[]>([]);
  const [findings, setFindings] = useState<Finding[]>([]);
  const [openDiff, setOpenDiff] = useState<string | null>(null);
  const [diffText, setDiffText] = useState("");
  const [diffFiles, setDiffFiles] = useState<DiffFile[]>([]);
  const [revertTarget, setRevertTarget] = useState<ChangeSet | null>(null);
  const [executorName, setExecutorName] = useState("internal");

  const load = useCallback(async () => {
    const [t, c, f, ex] = await Promise.all([
      get<Task[]>(`/projects/${projectId}/initiatives/${initiativeId}/tasks`).catch(() => []),
      get<ChangeSet[]>(`/projects/${projectId}/change-sets?initiative_id=${initiativeId}`).catch(() => []),
      get<Finding[]>(`/projects/${projectId}/findings?initiative_id=${initiativeId}`).catch(() => []),
      get<{ name: string; selected: boolean }[]>(`/projects/${projectId}/executors`).catch(() => []),
    ]);
    setTasks(t ?? []);
    setChangeSets(c ?? []);
    setFindings(f ?? []);
    const sel = (ex ?? []).find((e) => e.selected);
    if (sel) setExecutorName(sel.name);
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
    const id = setInterval(load, 2500);
    return () => clearInterval(id);
  }, [load]);

  const review = async (cs: ChangeSet, verdict: string) => {
    await post(`/projects/${projectId}/change-sets/${cs.id}/review`, { verdict });
    await load();
  };
  const apply = async (cs: ChangeSet) => {
    await post(`/projects/${projectId}/change-sets/${cs.id}/apply`, {});
    await load();
  };
  const revert = async (cs: ChangeSet) => {
    await post(`/projects/${projectId}/change-sets/${cs.id}/revert`, {});
    setRevertTarget(null);
    await load();
  };
  const resolveConflict = async (cs: ChangeSet, resolution: string) => {
    await post(`/projects/${projectId}/change-sets/${cs.id}/resolve-conflict`, { resolution });
    await load();
  };
  const toggleDiff = async (cs: ChangeSet) => {
    if (openDiff === cs.id) {
      setOpenDiff(null);
      setDiffText("");
      setDiffFiles([]);
      return;
    }
    setOpenDiff(cs.id);
    try {
      const raw = await apiRaw("GET", `/projects/${projectId}/change-sets/${cs.id}/diff`);
      try {
        const parsed = JSON.parse(raw);
        if (parsed && typeof parsed === "object") {
          if (Array.isArray(parsed.files)) setDiffFiles(parsed.files);
          else setDiffFiles([]);
          setDiffText(typeof parsed.patch === "string" ? parsed.patch : "");
          return;
        }
      } catch {
        /* plain text */
      }
      setDiffFiles([]);
      setDiffText(raw);
    } catch {
      setDiffFiles([]);
      setDiffText("");
    }
  };

  // A system-deny finding targets the change set and carries a DenyCode.
  const denyFor = (cs: ChangeSet) =>
    findings.find((f) => f.target_type === "change_set" && f.target_id === cs.id && f.code);

  return (
    <div className="phase-page">
      <PhaseHeader
        title="Build"
        purpose="Nothing touches your repo until you approve it."
        meta={`${executorName} executor · placeholder code`}
      />

      {changeSets.map((cs) => {
        const deny = denyFor(cs);
        if (deny) {
          return (
            <section key={cs.id} className="deny-card">
              <div className="deny-head">
                <span className="deny-chip">SYSTEM DENY</span>
                <span className="deny-title">
                  {cs.human_id} was rejected before review — {deny.finding}
                </span>
                <span className="deny-code">{deny.code}</span>
              </div>
              <div className="deny-desc">
                A deterministic boundary rejected this change set. It cannot be approved, overridden,
                or applied — system deny beats human approve. The task will be re-planned.
              </div>
              {(deny.evidence || deny.category) && (
                <div className="deny-evidence">{deny.evidence || deny.category}</div>
              )}
            </section>
          );
        }
        return (
          <section key={cs.id} className="cs-card">
            <div className="cs-row">
              <span className="cs-code">{cs.human_id}</span>
              <div className="cs-body">
                <div className="cs-title">{tasks.find((t) => t.id === cs.task_id)?.title ?? cs.task_id}</div>
                {cs.patch_digest && <div className="cs-meta">digest {cs.patch_digest.slice(0, 12)}…</div>}
              </div>
              <span className={`cs-pill pill-${STATUS_PILL[cs.status]?.state ?? "inactive"}`}>
                {STATUS_PILL[cs.status]?.label ?? cs.status}
              </span>
              <button className="cs-action" onClick={() => toggleDiff(cs)}>
                {openDiff === cs.id ? "Hide diff" : "View diff"}
              </button>
            </div>
            {openDiff === cs.id && (
              <div className="cs-files">
                {diffFiles.length === 0 ? (
                  <pre className="cs-diff">{diffText || "(no diff)"}</pre>
                ) : (
                  diffFiles.map((f) => (
                    <div key={f.path} className="cs-file">
                      <div className="cs-file-head">
                        <span className={`cs-file-badge cs-file-${f.change_type}`}>
                          {DIFF_BADGE[f.change_type] ?? f.change_type}
                        </span>
                        <span className="cs-file-path">{f.path}</span>
                        {f.change_type !== "delete" && (
                          <span className="cs-file-size">{f.size ?? 0} B</span>
                        )}
                      </div>
                      {f.change_type === "delete" ? (
                        <div className="cs-file-deleted">file deleted</div>
                      ) : (
                        <pre className="cs-file-content">{f.content || "(no content)"}</pre>
                      )}
                    </div>
                  ))
                )}
              </div>
            )}
            <div className="cs-actions">
              {cs.status === "review_required" && (
                <>
                  <button className="cs-green" onClick={() => review(cs, "approved")}>Approve</button>
                  <button className="cs-secondary" onClick={() => review(cs, "changes_requested")}>Request changes</button>
                  <button className="cs-secondary" onClick={() => review(cs, "rejected")}>Reject</button>
                </>
              )}
              {cs.status === "approved" && (
                <button className="cs-green" onClick={() => apply(cs)}>Apply</button>
              )}
              {cs.status === "applied" && (
                <button className="cs-danger" onClick={() => setRevertTarget(cs)}>Revert</button>
              )}
              {cs.status === "conflicted" && (
                <>
                  <button className="cs-secondary" onClick={() => resolveConflict(cs, "retry")}>Retry apply</button>
                  <button className="cs-danger" onClick={() => resolveConflict(cs, "reject")}>Reject</button>
                </>
              )}
            </div>
          </section>
        );
      })}

      {changeSets.length === 0 && <p className="muted">No change sets yet.</p>}

      {revertTarget && (
        <section className="revert-card">
          <div className="revert-title">Revert {revertTarget.human_id}?</div>
          <div className="revert-desc">
            Restores the files to their exact pre-apply bytes. This is a file-level restore, not a git
            revert — your git history is untouched. The requirement returns to approved and its
            evidence is cleared.
          </div>
          <div className="revert-actions">
            <button className="cs-danger" onClick={() => revert(revertTarget)}>Revert byte-for-byte</button>
            <button className="cs-secondary" onClick={() => setRevertTarget(null)}>Cancel</button>
          </div>
        </section>
      )}
    </div>
  );
}
