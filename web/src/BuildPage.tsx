import { useCallback, useEffect, useState } from "react";
import { apiRaw, get, post, resource } from "./api";
import PhaseHeader from "./PhaseHeader";
import FailedBanner from "./FailedBanner";
import {
  advisoryBacklogLabel,
  appliedOutcomeLabel,
  approveErrorMessage,
  buildFailure,
  buildHeaderMeta,
  canOverrideSecret,
  conflictCause,
  sortFindingsForCard,
  diffChangeTypeLabel,
  errMessage,
  executeRequestBody,
  findingCategoryLabel,
  gitPrErrorMessage,
  hasNoFilesChanged,
  isOperationInFlight,
  isWholeFileRewrite,
  noDiffMessage,
  notASecretConfirmCopy,
  partitionAdvisory,
  patchLines,
  prOverallLabel,
  prOverallState,
  revertRefusalMessage,
  runButtonState,
  type NextTask,
  type PartialWork,
  type PRChecksResultLike,
} from "./buildLogic";
import { isHttpUrl } from "./url";

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
  // model is set once the backend records which model produced the change
  // set (G3: a demo-model change set never reads SHIPPED, whatever executor
  // ran it). Absent from an older daemon -- treated as "not demo" then.
  model?: string;
  // executor is the executor KIND that actually produced this change set
  // (backend contract addition). appliedOutcomeLabel must read this, not the
  // page's currently-selected executor -- see buildLogic.ts.
  executor?: string;
  // agent_output is the end of what the coding agent printed in the run that
  // made this change set. The running card showed it live and disappeared
  // with it when the run finished.
  agent_output?: string;
  // git_commit_warning (review C4, task 3): set instead of a commit sha when
  // git_commit is on but the commit-on-apply guard refused (detached HEAD, a
  // rebase/merge/cherry-pick in progress, or the default branch without
  // git.allow_default_branch). The change set still applied; only the commit
  // was skipped. Absent from a daemon that hasn't shipped it, or when
  // nothing was skipped.
  git_commit_warning?: string;
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
  // Secret-sweep fields (review H3, task 1) -- set only on a secret_sweep
  // finding raised from a per-file credential match. Optional so a finding
  // from an older daemon, or of any other category, renders exactly as
  // before.
  secret_path?: string;
  secret_kind?: string;
  secret_preview?: string;
  secret_overridable?: boolean;
  // advisory (review B1/B2, task 2): never blocks approval, whatever the
  // severity. path/in_diff are the self-report that can produce it; the page
  // only needs `advisory` itself to decide where a finding renders.
  advisory?: boolean;
  path?: string;
  in_diff?: boolean;
  note?: string;
}
interface DiffFile {
  path: string;
  change_type: string;
  digest?: string;
  size?: number;
  content?: string;
  // patch is the file's own unified diff (H1: git-style hunks, ' '/'-'/'+'
  // prefixed lines, no ---/+++ headers). B6: rendering the final `content`
  // alone can't show what was removed, which is exactly how a deleted route
  // hid inside a modified file that looked ADDED. Falls back to `content`
  // against a daemon that hasn't shipped per-file patches yet, or that left
  // `patch` empty because the file was too large/binary to diff.
  patch?: string;
  // patch_note explains why `patch` is empty (too large, binary) -- H1.
  // Shown above the `content` fallback so an empty diff area doesn't read as
  // "nothing changed" when really "this couldn't be diffed".
  patch_note?: string;
}
// Initiative is the slice BuildPage needs off the initiative resource. Both
// fields are optional so this page works against a daemon that hasn't shipped
// them yet -- `next_task` null/absent just means nothing to run, and an empty
// `run_blocked_reason` just means running isn't blocked.
interface Initiative {
  next_task?: NextTask | null;
  run_blocked_reason?: string;
  // C3: what a stopped run left for the next run of its task to continue
  // from, straight off guidance -- omitted (never null) when nothing is
  // kept, the same "omit rather than emit null" contract as next_task.
  partial_work?: PartialWork;
  // git_branch / pr_url (review C4 piece 3, task 3): the initiative's
  // dedicated branch, set on first apply once git.branch_per_initiative is
  // on, and the PR url once `idea pr` / POST .../pr has opened one. Both
  // optional -- absent whatever the setting says before that first apply,
  // or before a PR has been opened.
  git_branch?: string;
  pr_url?: string;
}
interface Operation {
  id: string;
  type: string;
  status: string;
  created_at: string;
  // error is the redacted reason a failed operation failed (B2's contract).
  error?: string;
}

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
  const [initiative, setInitiative] = useState<Initiative | null>(null);
  const [operations, setOperations] = useState<Operation[]>([]);
  const [runBusy, setRunBusy] = useState(false);
  // Set when a run is accepted, cleared once the running card has had a poll
  // to appear. The Run button vanished on accept and the card only arrived on
  // its next poll, so for a few seconds the click looked ignored.
  const [startedTask, setStartedTask] = useState<string | null>(null);
  useEffect(() => {
    if (!startedTask) return;
    const id = setTimeout(() => setStartedTask(null), 6000);
    return () => clearTimeout(id);
  }, [startedTask]);
  const [runError, setRunError] = useState("");
  // Per-change-set busy/error (E2): every action button here disables its own
  // buttons in flight and catches its own failure inline, without freezing or
  // silencing any other change set's card.
  const [csBusy, setCsBusy] = useState<Record<string, string>>({});
  const [csError, setCsError] = useState<Record<string, string>>({});
  // runNote (task 5, D-a): the optional "Note to the agent" appended to the
  // next execute's brief. One shared field -- the Run/Continue button, a
  // denied change set's "Run the task again", and a rejected change set's
  // "Run the task again" all start the SAME next task, so they share the
  // same note rather than each keeping (and losing sync with) its own.
  const [runNote, setRunNote] = useState("");
  // Secret-sweep override state (task 1), keyed by finding id: `confirming`
  // shows the explanation before the disposition is posted; `busy`/`error`
  // guard the POST itself; `done` switches the card to "Run the task again"
  // once the disposition is recorded.
  const [secretConfirming, setSecretConfirming] = useState<Record<string, boolean>>({});
  const [secretBusy, setSecretBusy] = useState<Record<string, boolean>>({});
  const [secretError, setSecretError] = useState<Record<string, string>>({});
  const [secretDone, setSecretDone] = useState<Record<string, boolean>>({});
  // Git / PR state (task 3).
  const [prBusy, setPrBusy] = useState(false);
  const [prError, setPrError] = useState("");
  const [prChecks, setPrChecks] = useState<PRChecksResultLike | null>(null);
  const [prChecksError, setPrChecksError] = useState("");

  const load = useCallback(async () => {
    const [t, c, f, ex, ini, ops, guide] = await Promise.all([
      get<Task[]>(`/projects/${projectId}/initiatives/${initiativeId}/tasks`).catch(() => []),
      get<ChangeSet[]>(resource(projectId, initiativeId, "change-sets")).catch(() => []),
      get<Finding[]>(resource(projectId, initiativeId, "findings")).catch(() => []),
      get<{ name: string; selected: boolean }[]>(`/projects/${projectId}/executors`).catch(() => []),
      get<Initiative>(`/projects/${projectId}/initiatives/${initiativeId}`).catch(() => null),
      get<Operation[]>(`/projects/${projectId}/initiatives/${initiativeId}/operations`).catch(() => []),
      // next_task, run_blocked_reason and partial_work (C3) are served by the
      // guidance endpoint, which already computes "what should happen next";
      // merged onto the initiative so the rest of the page reads one object.
      get<Pick<Initiative, "next_task" | "run_blocked_reason" | "partial_work">>(
        `/projects/${projectId}/initiatives/${initiativeId}/guidance`,
      ).catch(() => null),
    ]);
    setTasks(t ?? []);
    setChangeSets(c ?? []);
    setFindings(f ?? []);
    const sel = (ex ?? []).find((e) => e.selected);
    if (sel) setExecutorName(sel.name);
    setInitiative(
      ini
        ? {
            ...ini,
            next_task: guide?.next_task ?? null,
            run_blocked_reason: guide?.run_blocked_reason ?? "",
            partial_work: guide?.partial_work,
          }
        : null,
    );
    setOperations(ops ?? []);
    // The PR's CI status (task 3) is only meaningful once a PR exists AND
    // there is a change set to ask through (every change set of the
    // initiative shares the same PR/result, per the contract) -- a 409 ("no
    // gh" or "no PR yet") is expected rather than an error worth surfacing
    // loudly every poll, so it's kept but shown quietly.
    const anyChangeSet = c?.[0]?.id;
    if (ini?.pr_url && anyChangeSet) {
      try {
        const checks = await get<PRChecksResultLike>(
          `/projects/${projectId}/change-sets/${anyChangeSet}/pr-checks`,
        );
        setPrChecks(checks);
        setPrChecksError("");
      } catch (e) {
        setPrChecks(null);
        setPrChecksError(gitPrErrorMessage(errMessage(e)));
      }
    } else {
      setPrChecks(null);
      setPrChecksError("");
    }
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
    const id = setInterval(load, 2500);
    return () => clearInterval(id);
  }, [load]);

  // runAction is the one place every change-set action (Approve, Request
  // changes, Apply, Revert, Resolve conflict) goes through: it marks that
  // action busy on that change set only, catches its failure into that
  // change set's own inline error, and always reloads after. Before this,
  // apply/revert/resolve had no try/catch at all -- a failure was a silent
  // dead click (E2).
  const runAction = async (
    cs: ChangeSet,
    action: string,
    fn: () => Promise<void>,
    // formatError lets a caller pick the right rewrite for its own 409s --
    // revert's refusal (task 4) already names files and advice and just
    // needs the CLI-oriented prefix stripped, which is not what
    // approveErrorMessage's "approval blocked" pattern is for.
    formatError: (raw: string) => string = approveErrorMessage,
  ) => {
    setCsBusy((b) => ({ ...b, [cs.id]: action }));
    setCsError((e) => ({ ...e, [cs.id]: "" }));
    try {
      await fn();
    } catch (e) {
      setCsError((prev) => ({ ...prev, [cs.id]: formatError(errMessage(e)) }));
    } finally {
      setCsBusy((b) => {
        const next = { ...b };
        delete next[cs.id];
        return next;
      });
      await load();
    }
  };

  // acceptFindings signs off the adversarial reviewer's open findings on this
  // change set and approves without re-reviewing. Only the reviewer's: a finding
  // carrying a deny code is untouched and still refuses, which is the panel
  // above this one.
  const review = (cs: ChangeSet, verdict: string, acceptFindings = false) => {
    const action =
      verdict === "approved" ? (acceptFindings ? "approve-accept" : "approve")
      : verdict === "changes_requested" ? "changes"
      : "reject";
    return runAction(cs, action, async () => {
      await post(`/projects/${projectId}/change-sets/${cs.id}/review`,
        acceptFindings ? { verdict, accept_findings: true } : { verdict });
    });
  };
  const apply = (cs: ChangeSet) =>
    runAction(cs, "apply", async () => {
      await post(`/projects/${projectId}/change-sets/${cs.id}/apply`, {});
    });
  const revert = (cs: ChangeSet) =>
    runAction(
      cs,
      "revert",
      async () => {
        await post(`/projects/${projectId}/change-sets/${cs.id}/revert`, {});
        setRevertTarget(null);
      },
      // task 4: the 409 already names the files and the advice -- just read
      // it, don't run it through the "approval blocked" rewrite.
      revertRefusalMessage,
    );
  const resolveConflict = (cs: ChangeSet, resolution: string) =>
    runAction(cs, `resolve-${resolution}`, async () => {
      await post(`/projects/${projectId}/change-sets/${cs.id}/resolve-conflict`, { resolution });
    });
  // "Reject and re-run" did only the first half; the user then had to find
  // the Run button. It rejects, then starts the task again from the current
  // workspace.
  const rejectAndRerun = async (cs: ChangeSet) => {
    await resolveConflict(cs, "reject");
    await runNext();
  };

  // startExecute is Run/Continue and "Start over"'s shared body: both end in
  // the same POST .../execute, and only "Start over" first discards what a
  // stopped run left (C3) so the next execute begins from the workspace
  // instead of continuing from those files.
  const startExecute = async (discardPartial: boolean) => {
    if (runBusy) return;
    setRunBusy(true);
    setRunError("");
    try {
      // One request: the daemon discards the kept files only once the run
      // is accepted. Deleting first and then being refused lost them.
      // task 5: whatever is in the shared "Note to the agent" field rides
      // along, appended to the implementer's brief.
      await post(
        `/projects/${projectId}/initiatives/${initiativeId}/execute`,
        executeRequestBody(discardPartial, runNote),
      );
      setStartedTask(initiative?.next_task?.human_id ?? "the next task");
      setRunNote("");
    } catch (e) {
      setRunError(errMessage(e));
    } finally {
      setRunBusy(false);
      await load();
    }
  };
  const runNext = () => startExecute(false);
  // "Start over" (C3): discards the stopped run's kept files first, so the
  // task runs from a clean workspace instead of resuming.
  const startOver = () => startExecute(true);

  // ---- task 1: secret denial -----------------------------------------

  const confirmNotASecret = (findingId: string) =>
    setSecretConfirming((c) => ({ ...c, [findingId]: true }));
  const cancelNotASecret = (findingId: string) =>
    setSecretConfirming((c) => ({ ...c, [findingId]: false }));

  // postNotASecret records the disposition (task 1): allowed at that exact
  // file from then on, and offers "Run the task again" once it lands.
  const postNotASecret = async (findingId: string) => {
    setSecretBusy((b) => ({ ...b, [findingId]: true }));
    setSecretError((e) => ({ ...e, [findingId]: "" }));
    try {
      await post(`/projects/${projectId}/findings/${findingId}/disposition`, {
        disposition: "not_a_secret",
      });
      setSecretDone((d) => ({ ...d, [findingId]: true }));
      setSecretConfirming((c) => ({ ...c, [findingId]: false }));
    } catch (e) {
      setSecretError((prev) => ({ ...prev, [findingId]: errMessage(e) }));
    } finally {
      setSecretBusy((b) => ({ ...b, [findingId]: false }));
      await load();
    }
  };

  // ---- task 3: git / PR -----------------------------------------------

  const createPR = async () => {
    setPrBusy(true);
    setPrError("");
    try {
      await post(`/projects/${projectId}/initiatives/${initiativeId}/pr`, {});
    } catch (e) {
      setPrError(gitPrErrorMessage(errMessage(e)));
    } finally {
      setPrBusy(false);
      await load();
    }
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

  const isRunning = operations.some((o) => isOperationInFlight(o.status));
  const failedExecute = buildFailure(operations);
  const runState = runButtonState(
    initiative?.next_task,
    initiative?.run_blocked_reason,
    isRunning,
    initiative?.partial_work,
  );
  // C3: nothing was kept for the next run to resume (the credential sweep
  // dropped it) -- said plainly instead of silently discarding it with no
  // explanation. Only when it's actually about the task about to run; a
  // partial for some other task is the daemon's to discard on its own.
  const droppedPartialNote =
    initiative?.partial_work &&
    initiative.partial_work.files === 0 &&
    initiative.partial_work.note &&
    initiative.partial_work.task_id === initiative?.next_task?.id
      ? initiative.partial_work.note
      : null;

  return (
    <div className="phase-page">
      <PhaseHeader
        title="Build"
        purpose="Nothing touches your repo until you approve it."
        meta={buildHeaderMeta(executorName)}
      />

      {/* task 3: the initiative's dedicated branch and its PR, once
          git.branch_per_initiative has created one. Absent (not just
          disabled) until then, and until the setting is on -- there's
          nothing here to show a project that isn't using it. */}
      {initiative?.git_branch && (
        <section className="git-pr-card">
          <div className="git-pr-row">
            <span className="git-pr-label">Branch</span>
            <span className="git-pr-branch">{initiative.git_branch}</span>
            {initiative.pr_url && isHttpUrl(initiative.pr_url) ? (
              <a className="git-pr-link" href={initiative.pr_url} target="_blank" rel="noreferrer">
                View pull request ↗
              </a>
            ) : initiative.pr_url ? (
              // H5, review fix: pr_url is the last non-empty line of `gh`'s
              // own output, not something the UI constructed -- a
              // non-http(s) value (however that happened) renders as plain
              // text, never as a clickable href.
              <span className="git-pr-link muted">{initiative.pr_url}</span>
            ) : (
              <button className="cs-secondary" disabled={prBusy} onClick={createPR}>
                {prBusy ? "Opening…" : "Create PR"}
              </button>
            )}
          </div>
          {prError && <p className="error">{prError}</p>}
          {initiative.pr_url && prChecks && (
            <>
              <div className={`git-pr-status pill-${prOverallState(prChecks.overall)}`}>
                {prOverallLabel(prChecks.overall)}
              </div>
              {prChecks.checks.length > 0 && (
                <ul className="git-pr-checks">
                  {prChecks.checks.map((c) => (
                    <li key={c.name}>
                      <span className="git-pr-check-name">{c.name}</span>
                      <span className="git-pr-check-state">{c.state}</span>
                      {/* H5, review fix: a check's `link` comes from the
                          third-party CI provider, not the UI -- only an
                          http(s) URL renders as a clickable href. */}
                      {c.link && isHttpUrl(c.link) && (
                        <a href={c.link} target="_blank" rel="noreferrer">
                          ↗
                        </a>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </>
          )}
          {/* task 3: "no gh" / "no pull request yet" read as readable
              messages, not a raw 409 -- shown quietly since it's expected
              until the PR exists or gh is installed. */}
          {initiative.pr_url && !prChecks && prChecksError && (
            <p className="muted">{prChecksError}</p>
          )}
        </section>
      )}

      {failedExecute && (
        <FailedBanner
          projectId={projectId}
          initiativeId={initiativeId}
          title="Build failed"
          reason={failedExecute.error}
          onRetry={async () => {
            // task 5: the shared "Note to the agent" field rides along here
            // too -- read at click time, not baked in when the banner was
            // rendered, so a note typed after the failure appeared is used.
            const res = await post<{ operation_id: string }>(
              `/projects/${projectId}/initiatives/${initiativeId}/execute`,
              executeRequestBody(false, runNote),
            );
            setRunNote("");
            return res.operation_id;
          }}
        />
      )}

      {!runState.visible && startedTask && (
        <div className="run-next-row">
          <span className="run-next-reason">Starting {startedTask}…</span>
        </div>
      )}
      {runState.visible && (
        <div className="run-next-row">
          <button className="cs-green" disabled={runState.disabled || runBusy} onClick={runNext}>
            {runBusy ? "Starting…" : runState.label}
          </button>
          {/* C3: "Start over" discards the stopped run's files first, then
              runs -- beside Continue rather than replacing it, since
              resuming is the default. */}
          {runState.startOver && (
            <button className="cs-secondary" disabled={runState.disabled || runBusy} onClick={startOver}>
              Start over
            </button>
          )}
          {runState.disabled && initiative?.run_blocked_reason && (
            <span className="run-next-reason">{initiative.run_blocked_reason}</span>
          )}
        </div>
      )}
      {/* task 5 (D-a): an optional note appended to the implementer's brief
          -- shared by the Run/Continue button above and by "Run the task
          again" on a denied or rejected change set below, since all three
          start the same next task. */}
      {runState.visible && !runState.disabled && (
        <div className="run-note-row">
          <textarea
            className="text-input run-note-input"
            placeholder="Note to the agent (optional) — e.g. what to try differently this time"
            value={runNote}
            onChange={(e) => setRunNote(e.target.value)}
            rows={2}
          />
        </div>
      )}
      {droppedPartialNote && (
        <p className="muted run-next-reason">
          The stopped run's files were not kept — {droppedPartialNote}.
        </p>
      )}
      {runError && <p className="error">{runError}</p>}

      {changeSets.map((cs) => {
        const deny = denyFor(cs);
        if (deny) {
          const isSecret = deny.code === "secret_sweep";
          // task 1: the button only ever renders when the backend itself
          // says this exact finding is eligible -- a value drawn from the
          // project's own configured secrets refuses the disposition
          // outright, so there is nothing to offer for it.
          const overridable = canOverrideSecret(deny);
          const confirming = !!secretConfirming[deny.id];
          const busy = !!secretBusy[deny.id];
          const done = !!secretDone[deny.id];
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
                {isSecret ? (
                  <>
                    A deterministic boundary rejected this change set. It cannot be approved or
                    applied — system deny beats human approve. If the flagged value is not really a
                    secret, mark it "not a secret" below to allow it, then run the task again;
                    otherwise remove it and run the task again.
                  </>
                ) : (
                  <>
                    A deterministic boundary rejected this change set. It cannot be approved,
                    overridden, or applied — system deny beats human approve. Run the task again.
                  </>
                )}
              </div>
              {/* task 1: location and kind, not just the generic evidence
                  blob -- what the ICP journey found missing: "no location and
                  no way through". */}
              {isSecret && (deny.secret_path || deny.secret_kind || deny.secret_preview) && (
                <dl className="secret-detail">
                  {deny.secret_path && (
                    <div className="secret-detail-row">
                      <dt>Location</dt>
                      <dd className="secret-detail-mono">{deny.secret_path}</dd>
                    </div>
                  )}
                  {deny.secret_kind && (
                    <div className="secret-detail-row">
                      <dt>Kind</dt>
                      <dd>{deny.secret_kind}</dd>
                    </div>
                  )}
                  {deny.secret_preview && (
                    <div className="secret-detail-row">
                      <dt>Value</dt>
                      <dd className="secret-detail-mono">{deny.secret_preview}</dd>
                    </div>
                  )}
                </dl>
              )}
              {(deny.evidence || deny.category) && (
                <div className="deny-evidence">
                  {isSecret && deny.evidence ? (
                    <>
                      <div className="secret-detail-label">Masked line</div>
                      {deny.evidence}
                    </>
                  ) : (
                    deny.evidence || deny.category
                  )}
                </div>
              )}
              {isSecret && overridable && !done && (
                <div className="secret-override">
                  {!confirming ? (
                    <button className="cs-secondary" onClick={() => confirmNotASecret(deny.id)}>
                      Not a secret
                    </button>
                  ) : (
                    <div className="secret-confirm">
                      <p>{notASecretConfirmCopy(deny.secret_path)}</p>
                      <div className="secret-confirm-actions">
                        <button className="cs-danger" disabled={busy} onClick={() => postNotASecret(deny.id)}>
                          {busy ? "Allowing…" : "Yes, allow this value"}
                        </button>
                        <button className="cs-secondary" disabled={busy} onClick={() => cancelNotASecret(deny.id)}>
                          Cancel
                        </button>
                      </div>
                      {secretError[deny.id] && <p className="error">{secretError[deny.id]}</p>}
                    </div>
                  )}
                </div>
              )}
              {/* task 1: "Run the task again" is always the way forward from
                  a deny card -- after a not_a_secret disposition, after
                  manually removing the value, or for any other deny code.
                  Hidden only while the confirm step above is open, so the
                  two actions don't compete for the click. */}
              {!confirming && (
                <div className="deny-actions">
                  <button className="cs-green" disabled={runBusy} onClick={runNext}>
                    {runBusy ? "Starting…" : "Run the task again"}
                  </button>
                </div>
              )}
            </section>
          );
        }
        const busyAction = csBusy[cs.id];
        const actionError = csError[cs.id];
        // Stale-base conflicts (Track A): a stale_base finding means retrying
        // would fail identically, so "Reject and re-run" must outrank
        // "Retry" rather than sit beside it as an equal option.
        const csFindings = findings.filter(
          (f) => f.target_type === "change_set" && f.target_id === cs.id,
        );
        // task 2: advisory findings never render as blockers, whatever their
        // severity -- conflictCause and the "blocking" list both read
        // blockingFindings, not the unpartitioned set.
        const { blocking: blockingFindings, advisory: advisoryFindings } = partitionAdvisory(csFindings);
        const cause = conflictCause(blockingFindings);
        // An agent that hit a 401 (or otherwise wrote nothing) is not
        // SHIPPED just because its executor/model both look real -- an
        // agent_output finding means the diff is genuinely empty, which
        // appliedOutcomeLabel alone has no way to know.
        const noFiles = hasNoFilesChanged(csFindings);
        const pillLabel =
          cs.status === "applied"
            ? noFiles
              ? "No changes"
              : appliedOutcomeLabel(cs.executor, cs.model)
            : STATUS_PILL[cs.status]?.label ?? cs.status;
        return (
          <section key={cs.id} className="cs-card">
            <div className="cs-row">
              <span className="cs-code">{cs.human_id}</span>
              <div className="cs-body">
                <div className="cs-title">{tasks.find((t) => t.id === cs.task_id)?.title ?? cs.task_id}</div>
                {cs.patch_digest && <div className="cs-meta">digest {cs.patch_digest.slice(0, 12)}…</div>}
              </div>
              <span className={`cs-pill pill-${STATUS_PILL[cs.status]?.state ?? "inactive"}`}>
                {pillLabel}
              </span>
              <button className="cs-action" onClick={() => toggleDiff(cs)}>
                {openDiff === cs.id ? "Hide diff" : "View diff"}
              </button>
            </div>
            {/* task 3: set instead of a commit sha when git_commit is on but
                the commit-on-apply guard refused -- the change set still
                applied, only the commit was skipped, which is easy to miss
                without this. */}
            {cs.git_commit_warning && (
              <div className="cs-git-warning">{cs.git_commit_warning}</div>
            )}
            {openDiff === cs.id && (
              <div className="cs-files">
                {diffFiles.length === 0 ? (
                  <pre className="cs-diff">{diffText || noDiffMessage(noFiles)}</pre>
                ) : (
                  diffFiles.map((f) => {
                    const rewrite = isWholeFileRewrite(f.change_type, f.patch ?? "");
                    // H1: patch is empty when the backend couldn't diff the
                    // file (too large, binary) -- patch_note says why, and
                    // the raw new content is shown underneath it rather than
                    // silently falling back with no explanation.
                    const lines = f.patch ? patchLines(f.patch) : [];
                    return (
                      <div key={f.path} className="cs-file">
                        <div className="cs-file-head">
                          <span className={`cs-file-badge cs-file-${f.change_type}`}>
                            {diffChangeTypeLabel(f.change_type)}
                          </span>
                          <span className="cs-file-path">{f.path}</span>
                          {rewrite && (
                            <span
                              className="cs-file-rewrite"
                              title="This patch replaces the entire file with no preserved lines"
                            >
                              whole-file rewrite
                            </span>
                          )}
                          {f.change_type !== "delete" && (
                            <span className="cs-file-size">{f.size ?? 0} B</span>
                          )}
                        </div>
                        {f.change_type === "delete" ? (
                          <div className="cs-file-deleted">file deleted</div>
                        ) : lines.length > 0 ? (
                          <pre className="cs-file-content">
                            {lines.map((line, i) => (
                              <div key={i} className={`cs-diff-line cs-diff-${line.kind}`}>
                                {line.text}
                              </div>
                            ))}
                          </pre>
                        ) : (
                          <>
                            {f.patch_note && <p className="muted cs-patch-note">{f.patch_note}</p>}
                            <pre className="cs-file-content">{f.content || "(no diff)"}</pre>
                          </>
                        )}
                      </div>
                    );
                  })
                )}
              </div>
            )}
            {/* H6: every finding targeting this change set, near the diff,
                in every status -- not only "conflicted" and not only
                workspace_copy/agent_output. Before this, an agent that hit a
                401 and changed nothing showed a bare "(no diff)" next to an
                Approve button with no explanation: the finding that said why
                existed but was never rendered outside a conflict. */}
            {cs.agent_output && (
              <details className="agent-output">
                <summary>Agent output</summary>
                <pre className="agent-output-pre">{cs.agent_output}</pre>
              </details>
            )}
            {/* task 2: an advisory finding (a rigor cap, evidence outside the
                diff, or unverified self-report) never renders as a blocker --
                it's split into its own collapsed group below, whatever its
                severity says. */}
            {blockingFindings.length > 0 && (
              <div className="cs-conflict-findings">
                {sortFindingsForCard(blockingFindings).map((f) => (
                  <div key={f.id} className={`cs-conflict-finding cs-conflict-${f.category}`}>
                    <span className="cs-conflict-cat">{findingCategoryLabel(f.category)}</span>
                    <span className="cs-conflict-text">
                      {f.finding}
                      {/* The finding alone said something changed but not what:
                          the file names are in the evidence. Short evidence
                          shows inline; long (agent output) folds. */}
                      {f.evidence &&
                        (f.evidence.length <= 400 ? (
                          <pre className="cs-finding-evidence">{f.evidence}</pre>
                        ) : (
                          <details>
                            <summary>Details</summary>
                            <pre className="cs-finding-evidence">{f.evidence}</pre>
                          </details>
                        ))}
                      {f.recommendation && <span className="cs-finding-rec">{f.recommendation}</span>}
                    </span>
                  </div>
                ))}
              </div>
            )}
            {advisoryFindings.length > 0 && (
              <details className="cs-backlog">
                <summary>{advisoryBacklogLabel(advisoryFindings.length)}</summary>
                <div className="cs-conflict-findings">
                  {advisoryFindings.map((f) => (
                    <div key={f.id} className={`cs-conflict-finding cs-conflict-${f.category}`}>
                      <span className="cs-conflict-cat">{findingCategoryLabel(f.category)}</span>
                      <span className="cs-conflict-text">
                        {f.finding}
                        {/* task 2: the backend's own note names the reason,
                            e.g. "about code this change set did not touch". */}
                        {f.note && <span className="cs-finding-rec">{f.note}</span>}
                        {f.recommendation && <span className="cs-finding-rec">{f.recommendation}</span>}
                      </span>
                    </div>
                  ))}
                </div>
              </details>
            )}
            <div className="cs-actions">
              {cs.status === "review_required" && (
                <>
                  <button className="cs-green" disabled={!!busyAction} onClick={() => review(cs, "approved")}>
                    {busyAction === "approve" ? "Reviewing…" : "Approve"}
                  </button>
                  {/* The escape. Without it, a change set the reviewer will not
                      pass has no route forward in this UI at all: Approve 400s,
                      Request changes regenerates into the same block, and Reject
                      throws the work away. */}
                  <button
                    className="cs-secondary"
                    disabled={!!busyAction}
                    onClick={() => review(cs, "approved", true)}
                  >
                    {busyAction === "approve-accept" ? "Reviewing…" : "Approve, accepting findings"}
                  </button>
                  <button
                    className="cs-secondary"
                    disabled={!!busyAction}
                    onClick={() => review(cs, "changes_requested")}
                  >
                    {busyAction === "changes" ? "Requesting…" : "Request changes"}
                  </button>
                  <button className="cs-secondary" disabled={!!busyAction} onClick={() => review(cs, "rejected")}>
                    {busyAction === "reject" ? "Rejecting…" : "Reject"}
                  </button>
                </>
              )}
              {cs.status === "approved" && (
                <button className="cs-green" disabled={!!busyAction} onClick={() => apply(cs)}>
                  {busyAction === "apply" ? "Applying…" : "Apply"}
                </button>
              )}
              {cs.status === "applied" && (
                <button className="cs-danger" disabled={!!busyAction} onClick={() => setRevertTarget(cs)}>
                  Revert
                </button>
              )}
              {/* task 5: a rejected change set used to be a dead end here --
                  no action at all -- once the reviewer's own "Reject" verdict
                  had been recorded. "Run the task again" (with the shared
                  note field above) is the way back. */}
              {cs.status === "rejected" && (
                <button className="cs-green" disabled={runBusy} onClick={runNext}>
                  {runBusy ? "Starting…" : "Run the task again"}
                </button>
              )}
              {cs.status === "conflicted" &&
                (cause === "stale_base" ? (
                  <>
                    <button
                      className="cs-danger"
                      disabled={!!busyAction}
                      onClick={() => rejectAndRerun(cs)}
                    >
                      {busyAction === "resolve-reject" ? "Rejecting…" : "Reject and re-run"}
                    </button>
                    <button
                      className="cs-secondary"
                      disabled={!!busyAction}
                      title="Applies only if the files named above are back to how they were when the agent ran"
                      onClick={() => resolveConflict(cs, "retry")}
                    >
                      {busyAction === "resolve-retry" ? "Retrying…" : "Retry after undoing my edits"}
                    </button>
                  </>
                ) : (
                  <>
                    <button
                      className="cs-secondary"
                      disabled={!!busyAction}
                      onClick={() => resolveConflict(cs, "retry")}
                    >
                      {busyAction === "resolve-retry" ? "Retrying…" : "Retry apply"}
                    </button>
                    <button
                      className="cs-danger"
                      disabled={!!busyAction}
                      onClick={() => resolveConflict(cs, "reject")}
                    >
                      {busyAction === "resolve-reject" ? "Rejecting…" : "Reject"}
                    </button>
                  </>
                ))}
            </div>
            {actionError && <p className="error">{actionError}</p>}
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
            <button
              className="cs-danger"
              disabled={csBusy[revertTarget.id] === "revert"}
              onClick={() => revert(revertTarget)}
            >
              {csBusy[revertTarget.id] === "revert" ? "Reverting…" : "Revert byte-for-byte"}
            </button>
            <button
              className="cs-secondary"
              disabled={csBusy[revertTarget.id] === "revert"}
              onClick={() => setRevertTarget(null)}
            >
              Cancel
            </button>
          </div>
          {csError[revertTarget.id] && <p className="error">{csError[revertTarget.id]}</p>}
        </section>
      )}
    </div>
  );
}
