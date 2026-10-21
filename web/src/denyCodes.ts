// The system-deny explanation table (docs/plans/milestone-6-system-deny.md,
// P0.3). A finding that carries a `code` is a deterministic system refusal,
// not a model's review opinion: it cannot be approved over, and the user needs
// to know what happened, why, and the one thing to do next — not a raw
// "system deny <code>" string.

export interface DenyExplanation {
  label: string;
  what: string;
  why: string;
  next: string;
  command?: string;
}

// DENY_CODES maps each domain.DenyCode to its human explanation. Codes the UI
// does not recognize fall through to a generic explanation (fail-open to "shown",
// never to "vanished" — the same rule judgmentScope.ts applies to findings).
export const DENY_CODES: Record<string, DenyExplanation> = {
  secret_sweep: {
    label: "secret found",
    what: "A produced file contains something that looks like a credential or secret.",
    why: "Secrets must never be written to the repository.",
    next: "Remove it and re-run, or, if it is not really a secret, mark it “not a secret”.",
    command: "idea disposition <id> not_a_secret",
  },
  pii: {
    label: "personal data",
    what: "A produced file contains personal data.",
    why: "Personal data is not stored in the repository.",
    next: "Redact it and re-run.",
  },
  path_escape: {
    label: "path escapes the workspace",
    what: "A diff path points outside the workspace.",
    why: "The change set may only touch files inside the project.",
    next: "Run the task again — this is a safety refusal, not a content defect.",
    command: "idea execute",
  },
  host_exec: {
    label: "build outside the sandbox",
    what: "A build/test step would have run outside the isolated check sandbox.",
    why: "Builds run in a network-isolated sandbox, never on the host.",
    next: "Set check.image (or a detected toolchain) and re-run.",
    command: "idea config validate",
  },
  blob_digest_mismatch: {
    label: "content hash mismatch",
    what: "Stored content does not match its expected digest.",
    why: "Content integrity failed.",
    next: "Run the task again.",
    command: "idea execute",
  },
  trust_deny: {
    label: "executor not authorized",
    what: "The selected executor's trust tier does not allow it to write files.",
    why: "An executor must be authorized before it can produce a change set.",
    next: "Choose an executor whose trust tier allows write_file.",
    command: "idea executors setup",
  },
  review_open_blocking: {
    label: "blocking finding open",
    what: "An undisposed critical/high finding is still open on the change set.",
    why: "A system deny cannot be overridden by a human approve.",
    next: "Disposition the finding, then approve — or accept the findings in one step.",
    command: "idea review <CHG-n> approve --accept-findings",
  },
  blast_radius: {
    label: "too many files",
    what: "The change set touches more files than execute.max_files allows.",
    why: "A change set is kept small enough to review and revert.",
    next: "Raise execute.max_files, or split the task into smaller slices.",
    command: "idea split-task <TASK-n>",
  },
};

export function isSystemDeny(code: string | undefined): code is string {
  return !!code && code in DENY_CODES;
}

// denyExplanation returns the human explanation for a deny code, or null when
// the finding carries no deny code (a model finding, not a system refusal).
export function denyExplanation(code: string | undefined): DenyExplanation | null {
  if (!code) return null;
  return DENY_CODES[code] ?? {
    label: "system deny",
    what: "The system refused this action.",
    why: "A deterministic safety boundary overrode the request.",
    next: "Check the logs for the full reason.",
  };
}

const DENY_ERROR_RE = /system deny ([a-z_]+)/;

// denyFromError recognizes a system-deny code inside a raw error string the
// daemon returned (e.g. "workflow: approval blocked: system deny
// review_open_blocking (open critical finding)"), so an action's error can
// render the same what/why/next as a finding that carries the code. Returns
// null when the message carries no known deny code.
export function denyFromError(message: string): DenyExplanation | null {
  const m = DENY_ERROR_RE.exec(message);
  return m ? denyExplanation(m[1]) : null;
}
