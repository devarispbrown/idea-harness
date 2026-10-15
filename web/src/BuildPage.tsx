import { useCallback, useEffect, useRef, useState } from "react";
import { apiRaw, get, post, resource } from "./api";
import PhaseHeader from "./PhaseHeader";
import FailedBanner from "./FailedBanner";
import QualityScorecard from "./QualityScorecard";
import LineagePanel from "./LineagePanel";
import RunPanel from "./RunPanel";
import BudgetMeter from "./BudgetMeter";
import {
  acceptableFindingCount,
  advisoryBacklogLabel,
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
  reviewRequestBody,
  prOverallLabel,
  prOverallState,
  reviewNeverCompleted,
  revertRefusalMessage,
  runButtonState,
  stripWorkflowPrefix,
  type NextTask,
  type PartialWork,
  type PRChecksResultLike,
} from "./buildLogic";
import { isHttpUrl } from "./url";
import { diffStat, initialCollapsed, narrowNow, toggled } from "./mobileLayout";
import { changeSetChip } from "./statusChips";
import NoteField from "./NoteField";
import InlinePanel from "./InlinePanel";
import { announce } from "./announce";

// What a screen reader hears when a change-set action lands (announce.ts):
// the chip changing colour is the only other sign.
const ACTION_DONE: Record<string, string> = {
  approve: "approved",
  "approve-accept": "approved, accepting its findings",
  changes: "sent back with your note",
  reject: "rejected",
  apply: "applied",
  revert: "reverted",
  "resolve-retry": "apply retried",
  "resolve-reject": "rejected",
  "rerun-review": "review re-run",
};

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
  // no_op (H8 fix): true when the change set touched zero files
  // (domain.ChangeSet.NoOp -- fsdiff found nothing to persist). An applied
  // no_op change set is not "SHIPPED" and never implemented its task --
  // appliedPillLabel and hasBuiltSomething (verify.ts) both read this.
  no_op?: boolean;
  // review_completed_at (H8 fix): unset means the adversarial review never
  // actually finished for this change set (Execute's own review call
  // errored, most often the run was cancelled mid-review) -- it carries only
  // mechanical-check observations. Approve always 400s in review_required
  // while this is unset; reviewNeverCompleted is the one place that reads it.
  review_completed_at?: string;
  // review_note: what the human asked for when they requested changes on (or
  // rejected) this change set. The task's next run carries it in its brief.
  review_note?: string;
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
  // redacted is true when `content` holds MASKED content instead of the
  // file's real bytes -- a change set the secret sweep denied never had its
  // raw bytes persisted, only a masked stand-in, so the human can still see
  // what was blocked without the raw secret ever reaching this page.
  redacted?: boolean;
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
  // Phone review (mobileLayout.ts): long lines wrap by default on a narrow
  // screen, and past the first few files a change set opens collapsed.
  const [wrapDiff, setWrapDiff] = useState(() => narrowNow());
  const [collapsedFiles, setCollapsedFiles] = useState<Set<string>>(() => new Set());
  const [revertTarget, setRevertTarget] = useState<ChangeSet | null>(null);
  const revertCancelRef = useRef<HTMLButtonElement>(null);
  const [executorName, setExecutorName] = useState("internal");
  const [initiative, setInitiative] = useState<Initiative | null>(null);
  const [operations, setOperations] = useState<Operation[]>([]);
  const [runBusy, setRunBusy] = useState(false);
  // Set when a run is accepted, cleared once the running card has had a poll
  // to appear. The Run button vanished on accept and the card only arrived on
  // its next poll, so for a few seconds the click looked ignored.
  const [startedTask, setStartedTask] = useState<string | null>(null);
  // How many change sets existed when the run was accepted. "Starting TASK-1…"
  // used to outlive the run itself: it stayed up for its full timeout even
  // after CHG-1 had landed. Any new change set, or the running card taking
  // over, now retires it.
  const [startedCount, setStartedCount] = useState(0);
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
  // Request changes takes a note (what to change), keyed by change set id:
  // `requesting` holds the open form's text. The note is recorded on the
  // change set and the task's next run carries it in its brief -- before
  // this, the button only flipped the status and the rerun had nothing new
  // to go on.
  const [requesting, setRequesting] = useState<Record<string, string>>({});
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
  // Returns whether fn() actually succeeded (H8 fix): a caller that chains a
  // second action off this one -- rejectAndRerun below -- needs to know that,
  // rather than treating "the promise settled" as "the request worked". This
  // used to always resolve (the catch swallowed the error into inline state
  // and there was nothing to return), so "Reject and re-run" started a fresh
  // agent run even when the reject itself 409'd.
  const runAction = async (
    cs: ChangeSet,
    action: string,
    fn: () => Promise<void>,
    // formatError lets a caller pick the right rewrite for its own 409s --
    // revert's refusal (task 4) already names files and advice and just
    // needs the CLI-oriented prefix stripped, which is not what
    // approveErrorMessage's "approval blocked" pattern is for.
    formatError: (raw: string) => string = approveErrorMessage,
  ): Promise<boolean> => {
    setCsBusy((b) => ({ ...b, [cs.id]: action }));
    setCsError((e) => ({ ...e, [cs.id]: "" }));
    let ok = true;
    try {
      await fn();
      announce(`${cs.human_id} ${ACTION_DONE[action] ?? "updated"}.`);
    } catch (e) {
      ok = false;
      const msg = formatError(errMessage(e));
      setCsError((prev) => ({ ...prev, [cs.id]: msg }));
      announce(`${cs.human_id}: ${msg}`);
    } finally {
      setCsBusy((b) => {
        const next = { ...b };
        delete next[cs.id];
        return next;
      });
      await load();
    }
    return ok;
  };

  // acceptFindings signs off the adversarial reviewer's open findings on this
  // change set and approves without re-reviewing. Only the reviewer's: a finding
  // carrying a deny code is untouched and still refuses, which is the panel
  // above this one.
  const review = (cs: ChangeSet, verdict: string, acceptFindings = false, note = "") => {
    const action =
      verdict === "approved" ? (acceptFindings ? "approve-accept" : "approve")
      : verdict === "changes_requested" ? "changes"
      : "reject";
    return runAction(cs, action, async () => {
      await post(
        `/projects/${projectId}/change-sets/${cs.id}/review`,
        reviewRequestBody(verdict, acceptFindings, note),
      );
    });
  };
  const openRequestChanges = (cs: ChangeSet) => setRequesting((r) => ({ ...r, [cs.id]: r[cs.id] ?? "" }));
  const closeRequestChanges = (cs: ChangeSet) =>
    setRequesting((r) => {
      const next = { ...r };
      delete next[cs.id];
      return next;
    });
  const sendRequestChanges = async (cs: ChangeSet) => {
    if (await review(cs, "changes_requested", false, requesting[cs.id] ?? "")) closeRequestChanges(cs);
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
  //
  // H8 fix: only when the reject actually succeeded. runAction used to
  // always resolve even on a failed request (the error was swallowed into
  // csError), so this ran a fresh, real agent run against a change set that
  // was still sitting there conflicted after a 409 -- a paid run starting
  // over an unresolved conflict it never actually cleared.
  const rejectAndRerun = async (cs: ChangeSet) => {
    const rejected = await resolveConflict(cs, "reject");
    if (!rejected) return;
    await runNext();
  };
  // rerunReview (H8 fix) is the escape for a change set stuck in
  // review_required whose adversarial review never actually completed
  // (reviewNeverCompleted): POST .../review {"verdict":"rerun"} is a real,
  // billed model call -- the button copy says so -- and is the only way this
  // change set can ever leave review_required, since approve refuses it
  // unconditionally in this state.
  const rerunReview = (cs: ChangeSet) =>
    runAction(cs, "rerun-review", async () => {
      await post(`/projects/${projectId}/change-sets/${cs.id}/review`, { verdict: "rerun" });
    }, stripWorkflowPrefix);

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
      setStartedCount(changeSets.length);
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
          if (Array.isArray(parsed.files)) {
            setDiffFiles(parsed.files);
            setCollapsedFiles(initialCollapsed(parsed.files.map((f: DiffFile) => f.path), narrowNow()));
          } else setDiffFiles([]);
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
      <RunPanel projectId={projectId} initiativeId={initiativeId} active={false} idleOnly />
      <BudgetMeter projectId={projectId} initiativeId={initiativeId} variant="build" onChanged={load} />

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
          onRetry={async (note) => {
            // The banner's own note field steers this retry; the shared run
            // note is the fallback for one typed before the failure appeared.
            const res = await post<{ operation_id: string }>(
              `/projects/${projectId}/initiatives/${initiativeId}/execute`,
              executeRequestBody(false, note || runNote),
            );
            setRunNote("");
            return res.operation_id;
          }}
        />
      )}

      {!runState.visible && startedTask && !isRunning && changeSets.length === startedCount && (
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
          <NoteField
            id={`run-note-${initiativeId}`}
            label={`Note to the agent for ${initiative?.next_task?.human_id ?? "this run"} (optional)`}
            placeholder="e.g. what to try differently this time — appended to the implementer's brief"
            value={runNote}
            onChange={setRunNote}
            onSubmit={runNext}
            submitLabel="run it"
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
        const chip = changeSetChip(cs, noFiles);
        const acceptable = acceptableFindingCount(csFindings);
        const requestNote = requesting[cs.id];
        return (
          <section key={cs.id} className="cs-card">
            <div className="cs-row">
              <span className="cs-code">{cs.human_id}</span>
              <div className="cs-body">
                <div className="cs-title">{tasks.find((t) => t.id === cs.task_id)?.title ?? cs.task_id}</div>
                {cs.patch_digest && <div className="cs-meta">digest {cs.patch_digest.slice(0, 12)}…</div>}
                <LineagePanel projectId={projectId} initiativeId={initiativeId} type="change_set" id={cs.id} code={cs.human_id} />
              </div>
              <span className={`cs-pill pill-${chip.state}`}>{chip.label}</span>
              {chip.marker && (
                <span className="cs-pill-marker" title={chip.marker === "demo" ? "Placeholder output: the demo model or the internal executor produced this, so no real code was written" : undefined}>
                  {chip.marker}
                </span>
              )}
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
              <div className={`cs-files${wrapDiff ? " cs-diff-wrap" : ""}`}>
                {diffFiles.length > 0 && (
                  <div className="cs-diff-toolbar">
                    <label className="cs-diff-wrap-toggle">
                      <input type="checkbox" checked={wrapDiff} onChange={(e) => setWrapDiff(e.target.checked)} />
                      Wrap long lines
                    </label>
                    {diffFiles.length > 1 && (
                      <button
                        type="button"
                        className="cs-action"
                        onClick={() =>
                          setCollapsedFiles(collapsedFiles.size > 0 ? new Set() : new Set(diffFiles.map((f) => f.path)))
                        }
                      >
                        {collapsedFiles.size > 0 ? "Expand all files" : "Collapse all files"}
                      </button>
                    )}
                  </div>
                )}
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
                    const collapsed = collapsedFiles.has(f.path);
                    const stat = f.patch ? diffStat(f.patch) : null;
                    return (
                      <div key={f.path} className={`cs-file${collapsed ? " cs-file-collapsed" : ""}`}>
                        <div className="cs-file-head">
                          <button
                            type="button"
                            className="cs-file-toggle"
                            aria-expanded={!collapsed}
                            aria-label={`${collapsed ? "Show" : "Hide"} ${f.path}`}
                            onClick={() => setCollapsedFiles((c) => toggled(c, f.path))}
                          >
                            {collapsed ? "▸" : "▾"}
                          </button>
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
                          {stat && (
                            <span className="cs-file-stat">
                              <span className="cs-file-stat-add">+{stat.added}</span>{" "}
                              <span className="cs-file-stat-del">−{stat.removed}</span>
                            </span>
                          )}
                          {f.change_type !== "delete" && (
                            <span className="cs-file-size">{f.size ?? 0} B</span>
                          )}
                        </div>
                        {collapsed ? null : f.change_type === "delete" ? (
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
                            {f.patch_note && (
                              <p className={`muted cs-patch-note${f.redacted ? " cs-patch-note-redacted" : ""}`}>
                                {f.patch_note}
                              </p>
                            )}
                            <pre className={`cs-file-content${f.redacted ? " cs-file-content-redacted" : ""}`}>
                              {f.content || "(no diff)"}
                            </pre>
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
            {/* H8 fix: a change set whose adversarial review never completed
                (Execute's own review call errored, most often the run was
                cancelled mid-review) used to render the same four buttons as
                a normally-reviewed one -- Approve and "Approve, accepting
                findings" both 400 unconditionally in this state
                (execute.go's ReviewCompleted() check runs before either
                approve path), with no explanation and no route forward. */}
            {cs.status === "review_required" && reviewNeverCompleted(cs) && (
              <p className="muted cs-review-incomplete-note">
                This change set's adversarial review never actually completed — most likely the run
                that produced it was cancelled mid-review. It carries only mechanical check
                observations, not a real review, so Approve will refuse until the review runs.
              </p>
            )}
            {/* On a phone the decision stays in reach: for a change set
                awaiting review, the actions and the note form below them
                stick to the bottom of the screen while the diff scrolls
                (styles.css, "Remote use and phones"). */}
            <div className={cs.status === "review_required" ? "cs-decision-bar" : undefined}>
            <div className="cs-actions">
              {cs.status === "review_required" &&
                (reviewNeverCompleted(cs) ? (
                  <>
                    <button
                      className="cs-green"
                      disabled={!!busyAction}
                      onClick={() => rerunReview(cs)}
                    >
                      {busyAction === "rerun-review" ? "Reviewing…" : "Re-run the review (costs a model call)"}
                    </button>
                    <button
                      className="cs-secondary"
                      disabled={!!busyAction || requestNote !== undefined}
                      onClick={() => openRequestChanges(cs)}
                    >
                      Request changes…
                    </button>
                    <button className="cs-secondary" disabled={!!busyAction} onClick={() => review(cs, "rejected")}>
                      {busyAction === "reject" ? "Rejecting…" : "Reject"}
                    </button>
                  </>
                ) : (
                  <>
                    <button className="cs-green" disabled={!!busyAction} onClick={() => review(cs, "approved")}>
                      {busyAction === "approve" ? "Reviewing…" : "Approve"}
                    </button>
                    {/* The escape. Without it, a change set the reviewer will not
                        pass has no route forward in this UI at all: Approve 400s,
                        Request changes regenerates into the same block, and Reject
                        throws the work away. */}
                    {acceptable > 0 && (
                      <button
                        className="cs-secondary"
                        disabled={!!busyAction}
                        onClick={() => review(cs, "approved", true)}
                      >
                        {busyAction === "approve-accept"
                          ? "Reviewing…"
                          : `Approve, accepting ${acceptable} finding${acceptable === 1 ? "" : "s"}`}
                      </button>
                    )}
                    <button
                      className="cs-secondary"
                      disabled={!!busyAction || requestNote !== undefined}
                      onClick={() => openRequestChanges(cs)}
                    >
                      Request changes…
                    </button>
                    <button className="cs-secondary" disabled={!!busyAction} onClick={() => review(cs, "rejected")}>
                      {busyAction === "reject" ? "Rejecting…" : "Reject"}
                    </button>
                  </>
                ))}
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
              {/* H8 fix: the backend explicitly accepts re-apply from
                  reverted (handlers.go's handleApplyChangeSet), subject to
                  the same staleness/secret-sweep guards as a fresh approved
                  change set -- this card used to render only the status pill
                  and no buttons at all, a dead end for a state the backend
                  already has a documented way out of. */}
              {cs.status === "reverted" && (
                <button className="cs-green" disabled={!!busyAction} onClick={() => apply(cs)}>
                  {busyAction === "apply" ? "Applying…" : "Re-apply"}
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
            {cs.status === "review_required" && requestNote !== undefined && (
              <InlinePanel
                className="request-changes-form"
                label={`Request changes to ${cs.human_id}`}
                onClose={() => closeRequestChanges(cs)}
              >
                <NoteField
                  id={`request-changes-${cs.id}`}
                  label={`What should change in ${cs.human_id}?`}
                  placeholder="Recorded on the change set; the task's next run gets it in its brief"
                  value={requestNote}
                  onChange={(v) => setRequesting((r) => ({ ...r, [cs.id]: v }))}
                  onSubmit={() => {
                    if (!busyAction && requestNote.trim()) sendRequestChanges(cs);
                  }}
                  submitLabel="send it back"
                />
                <div className="request-changes-actions">
                  <button
                    className="cs-green"
                    disabled={!!busyAction || !requestNote.trim()}
                    onClick={() => sendRequestChanges(cs)}
                  >
                    {busyAction === "changes" ? "Sending…" : "Send back with this note"}
                  </button>
                  {/* Rejecting can carry the same note: it is recorded on the
                      change set, and the task's next run gets it. */}
                  <button
                    className="cs-secondary"
                    disabled={!!busyAction || !requestNote.trim()}
                    onClick={async () => {
                      if (await review(cs, "rejected", false, requestNote)) closeRequestChanges(cs);
                    }}
                  >
                    {busyAction === "reject" ? "Rejecting…" : "Reject with this note"}
                  </button>
                  <button className="cs-secondary" disabled={!!busyAction} onClick={() => closeRequestChanges(cs)}>
                    Cancel
                  </button>
                </div>
              </InlinePanel>
            )}
            </div>
            {cs.review_note && (
              <p className="muted cs-review-note">
                You asked: “{cs.review_note}” —{" "}
                {cs.status === "changes_requested" || cs.status === "rejected"
                  ? "the task's next run carries this note."
                  : "the run after this one was given this note."}
              </p>
            )}
            {actionError && <p className="error">{actionError}</p>}
          </section>
        );
      })}

      {changeSets.length === 0 && <p className="muted">No change sets yet.</p>}

      {changeSets.some((c) => c.status === "applied") && (
        <QualityScorecard projectId={projectId} initiativeId={initiativeId} />
      )}

      {revertTarget && (
        // A confirmation that opens at the foot of the page, far from the
        // Revert button that asked for it: focus moves to its Cancel (the
        // safe choice), Escape cancels, and focus goes back to Revert.
        <InlinePanel
          className="revert-card"
          role="alertdialog"
          labelledBy="revert-title"
          initial={revertCancelRef}
          onClose={() => setRevertTarget(null)}
        >
          <div className="revert-title" id="revert-title">Revert {revertTarget.human_id}?</div>
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
              ref={revertCancelRef}
              className="cs-secondary"
              disabled={csBusy[revertTarget.id] === "revert"}
              onClick={() => setRevertTarget(null)}
            >
              Cancel
            </button>
          </div>
          {csError[revertTarget.id] && <p className="error">{csError[revertTarget.id]}</p>}
        </InlinePanel>
      )}
    </div>
  );
}
