#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# Workspace-local Go caches by default, so a sandboxed environment never writes
# to a home directory it may not own. Values already in the environment win: CI
# points these at the runner's cache, and forcing them back into the repo would
# re-download the whole module graph on every run.
export GOPATH="${GOPATH:-$ROOT/.gopath}"
export GOCACHE="${GOCACHE:-$ROOT/.buildcache}"
export GOMODCACHE="${GOMODCACHE:-$GOPATH/pkg/mod}"

go build -o "$ROOT/bin/idea" "$ROOT/cmd/idea"

# Workspace-local temp dirs (the daemon subprocess runs under the same
# workspace-scoped file sandbox, so it cannot write under /var/folders).
TMPROOT="$ROOT/.smoke/$$"
mkdir -p "$TMPROOT"
STATE="$TMPROOT/state"
PROJ="$TMPROOT/project"
mkdir -p "$STATE" "$PROJ"
export IDEA_HARNESS_STATE="$STATE"
# Unique port per run to avoid colliding with a lingering daemon.
export IDEA_HARNESS_ADDR="127.0.0.1:$((19000 + ($$ % 1000)))"

"$ROOT/bin/idea" daemon >"$STATE/daemon.log" 2>&1 &
DAEMON_PID=$!
trap 'kill "$DAEMON_PID" 2>/dev/null || true' EXIT

# wait_for <description> <timeout-seconds> <predicate...>
#
# Polls <predicate...> (a function name or command, invoked with no pipe
# needed since predicates are shell functions) every 0.1s until it exits 0,
# or fails loudly and exits the script non-zero once <timeout-seconds> have
# elapsed. Everything below that paces the async daemon goes through this
# instead of a fixed sleep, so the script runs as fast as the daemon allows
# and never silently races ahead of it.
wait_for() {
  local desc="$1" timeout="$2"
  shift 2
  local deadline=$((SECONDS + timeout))
  until "$@" >/dev/null 2>&1; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "ERROR: timed out after ${timeout}s waiting for $desc" >&2
      exit 1
    fi
    sleep 0.1
  done
}

# assert_out runs a command, then greps its combined output — instead of piping
# it into grep.
#
# `cmd | grep -q needle` under `set -o pipefail` fails **even when the grep
# matches**, and that is the whole of this file's intermittent-failure history.
#
# grep -q exits on the first match and closes the pipe. The Go binary is usually
# still writing -- `idea reviews` emits a heading and then three findings, one
# write(2) each -- so the next write gets EPIPE, and Go re-raises SIGPIPE on
# stdout rather than ignoring it. The process dies with 141, pipefail adopts
# that as the pipeline's status, and the `||` branch fires and prints a message
# saying the output was wrong when it was not.
#
# Measured against the real binary on a preserved failing state: 7 failures in
# 150 runs of `idea reviews | grep -q 'Review findings'`, with all three
# findings present in SQLite and printed correctly every time the command was
# run on its own. The same 150 runs in the shape below: 0.
#
# So: run the command, keep its output and status, then grep the string. Prints
# both on failure, plus a retry, because the two failures this replaced were
# undiagnosable from their own output.
#
# The `wait_for` predicates below still pipe into grep -q, deliberately: they
# return a status the poll loop reads, a spurious false costs one more
# iteration, and the loop has its own timeout.
assert_out() {
  local label="$1" needle="$2"; shift 2
  # `out=$(cmd); rc=$?` aborts here under `set -e` before anything is reported --
  # which is the case this helper exists for. An assignment with `||` is exempt.
  local out rc=0
  out=$("$@" 2>&1) || rc=$?
  if printf '%s' "$out" | grep -qF -- "$needle"; then return 0; fi
  echo "ERROR: $label"
  echo "  command: $*"
  echo "  exit status: $rc"
  echo "  looked for: $needle"
  echo "  output:"
  printf '%s\n' "$out" | sed 's/^/    /'
  # `|| true`: the retry is diagnostic output, and a second failure must not
  # abort before the exit below -- which would report the command's status
  # instead of the assertion's.
  echo "  retry:"
  { "$@" 2>&1 | sed 's/^/    /'; } || true
  exit 1
}

daemon_running() { "$ROOT/bin/idea" daemon status 2>/dev/null | grep -q running; }
# The two predicates below are negative ("the output no longer says X"), so they
# must first establish that there IS output: a command that errored or printed
# nothing would otherwise satisfy them on the first 0.1s poll and let the script
# race ahead of the daemon — something the fixed sleeps they replaced could not do.
initiative_left_running() {
  # `|| true`, not `|| return 1`: `idea status` exits non-zero when the newest
  # initiative has FAILED, and a failed initiative has certainly left the
  # running state. Treating that as "not yet" made wait_for poll to its timeout
  # and report `timed out waiting for initiative to leave the running state`
  # over an initiative that had already failed -- turning a legible failure into
  # a false timeout at the three call sites that use this.
  local out
  out=$("$ROOT/bin/idea" status 2>/dev/null) || true
  [ -n "$out" ] || return 1
  ! printf '%s' "$out" | grep -q running
}
tasks_present() {
  local out
  out=$("$ROOT/bin/idea" tasks 2>/dev/null) || return 1
  [ -n "$out" ] || return 1
  ! printf '%s' "$out" | grep -q "No tasks yet"
}
chg1_in_changes() { "$ROOT/bin/idea" changes 2>/dev/null | grep -q "CHG-1"; }
hello_txt_present() { test -f "$PROJ/hello.txt"; }
chg1_committed() { git -C "$PROJ" log --oneline 2>/dev/null | grep -q "CHG-1:"; }
chg1_reverted() { "$ROOT/bin/idea" change CHG-1 2>/dev/null | grep -q reverted; }
chg1_applied() { "$ROOT/bin/idea" change CHG-1 2>/dev/null | grep -q applied; }

# wait for health
wait_for "daemon to report running" 5 daemon_running

cd "$PROJ"
# Make the workspace a Git repo so the opt-in git-commit-on-apply path is
# exercised end-to-end (not just the byte-for-byte file revert).
git init -q
git config user.name "idea-harness-smoke"
git config user.email "smoke@example.invalid"
# On a feature branch: commit-on-apply refuses the repository's default
# branch unless git.allow_default_branch is set, so an agent never commits
# straight to main.
git checkout -q -b feature/smoke
"$ROOT/bin/idea" init
# Enable the opt-in git commit-on-apply flag, then restart the daemon so it
# re-loads the project config (the daemon caches config on first open).
sed -i.bak 's/git_commit: false/git_commit: true/' .idea-harness/harness.yaml
rm -f .idea-harness/harness.yaml.bak
kill "$DAEMON_PID" 2>/dev/null || true
wait "$DAEMON_PID" 2>/dev/null || true
"$ROOT/bin/idea" daemon >"$STATE/daemon.log" 2>&1 &
DAEMON_PID=$!
wait_for "daemon to report running (post-restart)" 5 daemon_running
echo "--- NEW ---"
"$ROOT/bin/idea" new "Build a recipe sharing app"
echo "--- waiting for workflow ---"
wait_for "initiative to leave the running state" 30 initiative_left_running
echo "--- STATUS ---"
"$ROOT/bin/idea" status
echo "--- DECISIONS ---"
"$ROOT/bin/idea" decisions
echo "--- CHANGED ---"
"$ROOT/bin/idea" changed
echo "--- DOCTOR ---"
"$ROOT/bin/idea" doctor
echo "--- MODEL LIST ---"
"$ROOT/bin/idea" model list
echo "--- PRD (head) ---"
"$ROOT/bin/idea" prd 2>&1 | head -20
echo "--- ADVANCE (M2) ---"
"$ROOT/bin/idea" advance
wait_for "the execution plan tasks to appear" 30 tasks_present
echo "--- TASKS ---"
"$ROOT/bin/idea" tasks
echo "--- WHY task TASK-1 ---"
"$ROOT/bin/idea" why task TASK-1
echo "--- IMPACT requirement REQ-1 ---"
"$ROOT/bin/idea" impact requirement REQ-1
echo "--- PLAN (head) ---"
"$ROOT/bin/idea" plan 2>&1 | head -12
echo "--- EXECUTE (M3) ---"
"$ROOT/bin/idea" execute --wait
wait_for "CHG-1 to appear in changes" 30 chg1_in_changes
echo "--- CHANGES ---"
"$ROOT/bin/idea" changes
echo "--- REVIEW approve CHG-1 ---"
"$ROOT/bin/idea" review CHG-1 approve
echo "--- APPLY CHG-1 ---"
"$ROOT/bin/idea" apply CHG-1
# apply is async; the applied file and the opt-in git commit land at
# (possibly) different points in that async work, so each gets its own poll
# rather than one sleep sized to cover both.
wait_for "hello.txt to be written by apply" 30 hello_txt_present
echo "--- WORKSPACE FILE (after apply) ---"
test -f "$PROJ/hello.txt" && echo "hello.txt present:" && cat "$PROJ/hello.txt"
echo "--- GIT COMMIT (after apply) ---"
# wait for the opt-in commit to land, then assert it exists and contains
# exactly the applied file (path-scoped, hooks-disabled).
wait_for "the CHG-1 git commit to land" 10 chg1_committed
git -C "$PROJ" log --oneline 2>/dev/null || true
git -C "$PROJ" log --oneline 2>/dev/null | grep -q "CHG-1:" || { echo "ERROR: no CHG-1 git commit after apply"; exit 1; }
git -C "$PROJ" ls-tree -r --name-only HEAD | grep -q hello.txt || { echo "ERROR: hello.txt not in the git commit"; exit 1; }
echo "--- CHANGE CHG-1 ---"
"$ROOT/bin/idea" change CHG-1
echo "--- REVERT CHG-1 ---"
"$ROOT/bin/idea" revert CHG-1
# revert is async and absence of a file has no positive signal to poll on, so
# wait for the change set to report reverted first, then assert the file is
# gone.
wait_for "CHG-1 to report reverted" 30 chg1_reverted
echo "--- WORKSPACE FILE (after revert) ---"
if [ -f "$PROJ/hello.txt" ]; then echo "ERROR: hello.txt still present after revert"; exit 1; else echo "hello.txt removed"; fi
echo "--- CHANGE CHG-1 (after revert) ---"
"$ROOT/bin/idea" change CHG-1
echo "--- VERIFY (B4/B10) ---"
"$ROOT/bin/idea" verify REQ-1 --pass --note "smoke"
"$ROOT/bin/idea" verify
echo "--- NEXT (Track C) ---"
"$ROOT/bin/idea" next
echo "--- RISK ---"
"$ROOT/bin/idea" risk
echo "--- EXECUTORS ---"
"$ROOT/bin/idea" executors

# ---------------------------------------------------------------------------
# Second pass: an EXISTING project.
#
# Everything above ran in an empty directory, which is what smoke.sh and
# smoke-real.sh have always done and what the README quick start shows. Nothing
# in the tree exercised `idea init` where files already exist, so the two claims
# docs/demo-existing-project.md makes to a reader about their own repository
# were untested:
#
#   1. applying a change set leaves pre-existing files byte-identical
#   2. reverting leaves the working tree pristine
#
# Both are asserted here against real content rather than narrated. This is the
# path where the product's headline properties actually matter: an empty
# directory has no pre-image to restore.
# ---------------------------------------------------------------------------
echo
echo "=== EXISTING-PROJECT PASS ==="

EPROJ="$TMPROOT/existing"
mkdir -p "$EPROJ/src"
cat >"$EPROJ/src/billing.py" <<'PY'
"""Usage billing for the API."""

RATE_PER_CALL = 0.002


def charge(calls: int) -> float:
    return calls * RATE_PER_CALL
PY
printf '# acme-api\n\nBilling lives in `src/billing.py`.\n' >"$EPROJ/README.md"

cd "$EPROJ"
git init -q
git config user.name "idea-harness-smoke"
git config user.email "smoke@example.invalid"
git add -A
git commit -qm "initial"
BEFORE_SUM=$(git hash-object src/billing.py)
BEFORE_HEAD=$(git rev-parse HEAD)

echo "--- INIT (into a repo that already has files) ---"
"$ROOT/bin/idea" init
for f in src/billing.py README.md; do
  test -f "$f" || { echo "ERROR: idea init removed $f"; exit 1; }
done
[ "$(git hash-object src/billing.py)" = "$BEFORE_SUM" ] ||
  { echo "ERROR: idea init modified src/billing.py"; exit 1; }
echo "existing files intact after init"

# Grounding goes through --doc: `idea docs add` needs an initiative to attach
# to, so it cannot come first. The guide documents that ordering; this pins it.
if "$ROOT/bin/idea" docs add src/billing.py >/dev/null 2>&1; then
  echo "ERROR: idea docs add succeeded before any initiative existed"; exit 1
fi
echo "docs add correctly refused before `idea new`"

echo "--- NEW (grounded on the existing source) ---"
"$ROOT/bin/idea" new "Add tiered pricing for high-volume callers" --doc src/billing.py
wait_for "the existing-project initiative to leave running" 30 initiative_left_running

echo "--- DOCS (the source file is attached) ---"
"$ROOT/bin/idea" docs
assert_out "--doc did not attach src/billing.py" "billing.py" "$ROOT/bin/idea" docs

echo "--- NEXT --until-human ---"
"$ROOT/bin/idea" next --until-human
wait_for "a change set in the existing project" 30 chg1_in_changes

echo "--- REVIEW + APPLY ---"
"$ROOT/bin/idea" review CHG-1 approve
"$ROOT/bin/idea" apply CHG-1
wait_for "the placeholder to land" 30 test -f "$EPROJ/hello.txt"

# The claim the guide asks a reader to confirm with md5. Asserted, not narrated:
# the default executor writes a placeholder and does not edit existing source.
[ "$(git hash-object src/billing.py)" = "$BEFORE_SUM" ] ||
  { echo "ERROR: apply modified src/billing.py; the default executor must not"; exit 1; }
echo "src/billing.py byte-identical after apply"

echo "--- REVERT ---"
"$ROOT/bin/idea" revert CHG-1
wait_for "the existing-project change set to read reverted" 30 chg1_reverted
test ! -f "$EPROJ/hello.txt" || { echo "ERROR: hello.txt survived revert"; exit 1; }
[ "$(git hash-object src/billing.py)" = "$BEFORE_SUM" ] ||
  { echo "ERROR: revert altered src/billing.py"; exit 1; }
[ "$(git rev-parse HEAD)" = "$BEFORE_HEAD" ] ||
  { echo "ERROR: the existing-project pass moved HEAD"; exit 1; }

# Pristine means: nothing dirty, and the only untracked entry is the config
# directory idea init added.
DIRTY=$(git status --porcelain | grep -v '^?? \.idea-harness/$' || true)
[ -z "$DIRTY" ] || { echo "ERROR: working tree not pristine after revert:"; echo "$DIRTY"; exit 1; }
echo "working tree pristine after revert (only .idea-harness/ untracked)"

echo "=== EXISTING-PROJECT PASS OK ==="

# ---------------------------------------------------------------------------
# PASS 3 — the human-judgment branch.
#
# The demo reviewer returned `{"findings":[]}` unconditionally, so the review
# loop always converged and `needs_human_judgment` was STRUCTURALLY UNREACHABLE
# on the only path this script, CI and six DX audits ever ran. A P0 that failed
# 4/4 on the README's own quick start survived six rounds in that blind spot.
#
# `demo.block_review_cycles` makes the PRD review not converge, so the branch is
# exercised on every push, with no secrets and no network.
# ---------------------------------------------------------------------------
echo "=== HUMAN-JUDGMENT PASS ==="
JPROJ="$TMPROOT/judgment"
mkdir -p "$JPROJ"
cd "$JPROJ"
"$ROOT/bin/idea" init
# max_cycles is 3 by default, so three blocking reviews exhaust the loop.
#
# The queue is primed when the daemon opens the project and is refilled on every
# reload, so this restart must stay the LAST config write before `idea new`.
# Another `idea model set` or executor write between here and there would refill
# it, and the surplus would spill past M1's review cycles.
printf '\ndemo:\n  block_review_cycles: 3\n' >>.idea-harness/harness.yaml
kill "$DAEMON_PID" 2>/dev/null || true
wait "$DAEMON_PID" 2>/dev/null || true
"$ROOT/bin/idea" daemon >"$STATE/daemon.log" 2>&1 &
DAEMON_PID=$!
wait_for "daemon to report running (judgment pass)" 5 daemon_running

# `idea new`'s OWN output, which this pass produced and never checked. It
# printed `✓ reviewed PRD ready` unconditionally on operation success, so a PRD
# parked for human judgment -- exactly what this pass sets up -- was reported as
# ready while `idea status` said "needs your judgment" one command later.
NEW_OUT=$("$ROOT/bin/idea" new "A recipe sharing app for home cooks")
printf '%s\n' "$NEW_OUT"
printf '%s' "$NEW_OUT" | grep -q 'reviewed PRD ready' &&
  { echo "ERROR: idea new reported a reviewed PRD over a review parked on a human"; exit 1; }
printf '%s' "$NEW_OUT" | grep -q 'did not converge' ||
  { echo "ERROR: idea new did not name the parked review"; exit 1; }
echo "idea new reports the parked review rather than a ready PRD"
wait_for "the blocked initiative to leave the running state" 60 initiative_left_running

# The pointer. `idea next` named `idea decisions` for every cause, and in this
# one `idea decisions` prints "No decisions yet." while the findings sit under
# `idea reviews` — which nothing named.
NEXT_OUT=$("$ROOT/bin/idea" next)
printf '%s\n' "$NEXT_OUT"
printf '%s' "$NEXT_OUT" | grep -q 'did not converge' ||
  { echo "ERROR: idea next did not report a non-converged PRD review"; exit 1; }
printf '%s' "$NEXT_OUT" | grep -q 'idea approve' ||
  { echo "ERROR: idea next did not name idea approve"; exit 1; }
printf '%s' "$NEXT_OUT" | grep -q 'idea reviews' ||
  { echo "ERROR: idea next did not name idea reviews, where the findings are"; exit 1; }
if printf '%s' "$NEXT_OUT" | grep -q 'idea decisions'; then
  echo "ERROR: idea next still points at idea decisions, which is empty here"; exit 1
fi
echo "idea next names the commands that move it"

# `grep -q .` would match "No open findings." -- cmdReviews prints that and
# returns nil -- so this greps for the heading it only prints when there are some.
assert_out "idea reviews shows no findings on a blocked review" "Review findings" "$ROOT/bin/idea" reviews
assert_out "idea decisions is not empty; the pointer test below is meaningless" "No decisions yet" "$ROOT/bin/idea" decisions
echo "the findings are under idea reviews, and idea decisions is empty — as the defect described"

echo "--- APPROVE ---"
"$ROOT/bin/idea" approve
"$ROOT/bin/idea" next --until-human
wait_for "the approved initiative to reach a change set" 60 chg1_in_changes

# Completion is the assertion that matters. `idea approve` used to leave every
# blocking finding undisposed, and the completion gate refuses while any exists —
# so the initiative reached exactly here and could never finish, with no CLI
# command able to disposition anything.
"$ROOT/bin/idea" review CHG-1 approve
"$ROOT/bin/idea" apply CHG-1
wait_for "the judgment-pass change set to read applied" 30 chg1_applied
# Three review cycles supersede REQ-1 and REQ-2, so the active code is not REQ-1.
#
# `|| true` because `idea verify` with no requirement exits non-zero when the
# gate is unmet, which is exactly the state here -- and under `set -e` with
# `pipefail` a failing command substitution kills the script with no message,
# because the error text went into the variable.
JREQ=$("$ROOT/bin/idea" verify 2>&1 | grep -oE 'REQ-[0-9]+' | head -1 || true)
[ -n "$JREQ" ] || { echo "ERROR: could not find the active requirement"; exit 1; }
echo "active requirement after 3 revise cycles: $JREQ"
"$ROOT/bin/idea" verify "$JREQ" --pass
assert_out "an approved-with-findings initiative did not reach complete" "initiative complete" "$ROOT/bin/idea" next
echo "a PRD approved with its findings accepted reaches complete"

# And the fourth judgment reason, which needs no provider change: failing a
# requirement after completion drops the initiative back out of complete.
"$ROOT/bin/idea" verify "$JREQ" --fail --note "re-evaluated after release" || true
VOUT=$("$ROOT/bin/idea" next)
printf '%s' "$VOUT" | grep -q 'failed after this initiative completed' ||
  { echo "ERROR: idea next did not report the post-completion downgrade: $VOUT"; exit 1; }
printf '%s' "$VOUT" | grep -q 'idea verify' ||
  { echo "ERROR: idea next did not name idea verify for a verification block"; exit 1; }
echo "a requirement failed after completion reports the verification reason"


# ---------------------------------------------------------------------------
# The demo guides are executable, and nothing executed them. Every one of their
# fenced output blocks was hand-written, and eight were wrong -- in the two
# documents the README tells everyone to start with.
#
# The ledger's `absent:` predicates catch a re-edit that drops these lines from
# the docs. They cannot catch the other direction: the binary changing while the
# docs stand still. This does, for the lines that carry a command the reader is
# told to run -- and it costs nothing, because this script has already produced
# exactly this output three passes above.
#
# Deliberately a handful of lines, not a full diff of every block. A full
# comparison needs an annotation language, a normaliser and a fixture corpus,
# for two documents whose blocks a person can re-derive in an afternoon; that
# was designed, reviewed, and judged not worth its surface.
# ---------------------------------------------------------------------------
echo "=== DEMO-GUIDE DRIFT ==="
doc_has() {
  local doc="$1" line="$2"
  grep -qF -- "$line" "$ROOT/docs/$doc" ||
    { echo "ERROR: the binary prints a line docs/$doc does not show:"; echo "  $line"; exit 1; }
}

# Captured from this pass, not retyped: these are the lines whose absence was
# the defect.
doc_has demo-greenfield.md "cancel with: idea cancel"
doc_has demo-greenfield.md "✓ evidence recorded for"
doc_has demo-greenfield.md "✓ revert started"
doc_has demo-greenfield.md "→ Proceed to design & execution plan"
doc_has demo-existing-project.md "→ advance …"
doc_has demo-existing-project.md "✓ revert started"
doc_has demo-existing-project.md "demo mode — synthetic output."

# The executor warning is one line in the binary and was hand-wrapped into two
# in both guides. ledger-check cannot see this -- it collapses a newline plus
# indent to a single space, which is exactly the difference -- so it is checked
# here, where the raw bytes are available.
#
# The warning is now the auto-executor fallback (executor.kind unset, demo mode:
# no real code will be written), not the old explicit-internal line.
for d in demo-greenfield.md demo-existing-project.md; do
  grep -qF -- "executor.kind is unset (auto) and no coding model is configured (demo mode), so this run uses the internal placeholder executor." "$ROOT/docs/$d" ||
    { echo "ERROR: docs/$d wraps the executor warning; the binary prints it on one line"; exit 1; }
done
echo "the demo guides show the lines the binary actually prints"

echo "=== HUMAN-JUDGMENT PASS OK ==="

# ---------------------------------------------------------------------------
# PASS 4 — the other two judgment reasons.
#
# `needs_human_judgment` has four recorded reasons and `idea next` gives each a
# different instruction, because the escapes differ. Pass 3 covers prd_review and
# verification. These two were reachable only in production: an AST census pinned
# which reason gets WRITTEN, and a CLI test pinned the rendered advice, but
# nothing drove the states.
#
# That gap hid a live P1. A plan review that blocked once and passed on a later
# run left its blocking findings open, so `idea approve-plan` succeeded and made
# the initiative unfinishable — the same omission already fixed twice on the PRD
# path. Only a behavioural pass finds that class.
# ---------------------------------------------------------------------------
echo "=== PLAN-JUDGMENT PASS ==="
PPROJ="$TMPROOT/planjudgment"
mkdir -p "$PPROJ"
cd "$PPROJ"
"$ROOT/bin/idea" init
# Same trap as pass 3: the queues are primed when the daemon opens the project
# and refilled on every config reload, so this restart must stay the LAST config
# write before `idea new`.
printf '\ndemo:\n  dangle_plan_dependency: true\n  block_plan_review_cycles: 1\n' >>.idea-harness/harness.yaml
kill "$DAEMON_PID" 2>/dev/null || true
wait "$DAEMON_PID" 2>/dev/null || true
"$ROOT/bin/idea" daemon >"$STATE/daemon.log" 2>&1 &
DAEMON_PID=$!
wait_for "daemon to report running (plan-judgment pass)" 5 daemon_running

"$ROOT/bin/idea" new "A meal planner for shared households"
wait_for "the PRD to converge" 60 initiative_left_running
"$ROOT/bin/idea" advance
wait_for "the plan-dependency block" 60 initiative_left_running

# Reason 3 of 4. approve-plan must NOT be offered: the dependency graph is known
# to be incomplete, and approving locks it in.
DEP_OUT=$("$ROOT/bin/idea" next)
printf '%s\n' "$DEP_OUT"
printf '%s' "$DEP_OUT" | grep -q 'unresolved task dependencies' ||
  { echo "ERROR: idea next did not report the dependency reason"; exit 1; }
printf '%s' "$DEP_OUT" | grep -q 'idea revise-plan' ||
  { echo "ERROR: idea next did not name revise-plan"; exit 1; }
if printf '%s' "$DEP_OUT" | grep -q 'idea approve-plan'; then
  echo "ERROR: idea next offered approve-plan for a plan whose graph is known incomplete"; exit 1
fi

"$ROOT/bin/idea" revise-plan
wait_for "the plan-review block" 60 initiative_left_running

# Reason 4 of 4.
PR_OUT=$("$ROOT/bin/idea" next)
printf '%s\n' "$PR_OUT"
printf '%s' "$PR_OUT" | grep -q 'plan review raised blocking findings' ||
  { echo "ERROR: idea next did not report the plan-review reason"; exit 1; }
printf '%s' "$PR_OUT" | grep -q 'idea reviews' ||
  { echo "ERROR: idea next did not name where the findings are"; exit 1; }
assert_out "idea reviews shows nothing on a blocked plan review" "Review findings" "$ROOT/bin/idea" reviews
echo "both remaining judgment reasons reported, each with its own instruction"

# And it finishes — escaping the plan review with approve-plan specifically.
#
# This asserts the disposition side-effect: if approve-plan does not disposition
# the findings that earlier block left open, the completion gate refuses forever
# and no CLI command can clear them. revise-plan would hide that, because it
# dispositions on its own way past.
#
# It does NOT assert that approve-plan escapes a review that keeps blocking, and
# it never could. This comment used to say approve-plan "re-runs the review; on
# the demo provider the queue is drained by now so it converges" — which was
# true, and was exactly the blind spot: on a converging queue, approve-plan
# succeeding is indistinguishable from the queue running out. DX audit 7 found a
# live-lock underneath it that six audits, CI and this script all passed over.
# The NON-CONVERGING pass below is what asserts the escape.
"$ROOT/bin/idea" approve-plan
"$ROOT/bin/idea" next --until-human
wait_for "a change set in the plan-judgment project" 60 chg1_in_changes
"$ROOT/bin/idea" review CHG-1 approve
"$ROOT/bin/idea" apply CHG-1
wait_for "the plan-judgment change set to read applied" 30 chg1_applied
PREQ=$("$ROOT/bin/idea" verify 2>&1 | grep -oE 'REQ-[0-9]+' | head -1 || true)
[ -n "$PREQ" ] || { echo "ERROR: could not find the active requirement"; exit 1; }
"$ROOT/bin/idea" verify "$PREQ" --pass
assert_out "a plan revised out of both judgment states did not reach complete" "initiative complete" "$ROOT/bin/idea" next
echo "a plan revised out of both judgment states reaches complete"

echo "=== PLAN-JUDGMENT PASS OK ==="

# ---------------------------------------------------------------------------
# PASS 5 — a plan review that never converges.
#
# This is the pass DX audit 7's P0-1 needed and no pass provided. Every earlier
# plan-judgment pass sizes demo.block_plan_review_cycles so the reviewer
# CONVERGES, and on a converging reviewer `idea approve-plan` reaching
# plan_approved proves nothing: it is indistinguishable from the queue running
# out. On a real provider the review kept blocking — observed eleven consecutive
# times — and approve-plan, which used to re-run it, had no route to
# plan_approved from any surface. The demo oracle could not express that, so six
# audits, CI and this script all passed over a live-lock in the documented path.
#
# 50 is not a converging count: nothing here makes fifty plan_review calls, so
# the queue cannot drain and a pass is attributable to the accept alone.
#
# dangle_plan_dependency stays OFF. With it on the initiative parks on
# plan_dependencies, which approve-plan refuses by design, and the pass would
# never reach the review it exists to test.
# ---------------------------------------------------------------------------
echo "=== NON-CONVERGING PLAN REVIEW PASS ==="
NPROJ="$TMPROOT/planlivelock"
mkdir -p "$NPROJ"
cd "$NPROJ"
"$ROOT/bin/idea" init
# The queues are primed when the daemon opens the project and refilled on every
# config reload, so this restart must stay the LAST config write before `idea new`.
printf '\ndemo:\n  block_plan_review_cycles: 50\n' >>.idea-harness/harness.yaml
kill "$DAEMON_PID" 2>/dev/null || true
wait "$DAEMON_PID" 2>/dev/null || true
"$ROOT/bin/idea" daemon >"$STATE/daemon.log" 2>&1 &
DAEMON_PID=$!
wait_for "daemon to report running (non-converging pass)" 5 daemon_running

"$ROOT/bin/idea" new "A bike-share pricing engine"
wait_for "the PRD to converge" 60 initiative_left_running
# No `|| true`: this exits non-zero only when the advance itself failed, and
# swallowing that would surface as the setup grep below failing instead, which
# names the wrong cause.
"$ROOT/bin/idea" next --until-human
wait_for "the plan review to park" 60 initiative_left_running

NOUT=$("$ROOT/bin/idea" next 2>&1 || true)
printf '%s' "$NOUT" | grep -q 'plan review raised blocking findings' ||
  { echo "ERROR: setup did not reach a blocked plan review:"; printf '%s\n' "$NOUT"; exit 1; }

# The assertion. A reviewer that will not converge, and one approve-plan.
"$ROOT/bin/idea" approve-plan
wait_for "approve-plan to settle" 60 initiative_left_running
# The status, read structurally. A prose grep with alternatives would pass on
# the rendered word "plan" and assert nothing.
NSTATUS=$("$ROOT/bin/idea" status --json | grep -o '"status": *"[a-z_]*"' | head -1)
[ "$NSTATUS" = '"status": "plan_approved"' ] ||
  { echo "ERROR: approve-plan did not accept a plan the reviewer keeps blocking (status=$NSTATUS)";
    "$ROOT/bin/idea" status; exit 1; }
echo "approve-plan accepts a plan review that never converges"

# And the accepted findings really are dispositioned, so completion is reachable.
"$ROOT/bin/idea" next --until-human
wait_for "a change set in the non-converging project" 60 chg1_in_changes
"$ROOT/bin/idea" review CHG-1 approve
"$ROOT/bin/idea" apply CHG-1
wait_for "the non-converging change set to read applied" 30 chg1_applied
NREQ=$("$ROOT/bin/idea" verify 2>&1 | grep -oE 'REQ-[0-9]+' | head -1 || true)
[ -n "$NREQ" ] || { echo "ERROR: could not find the active requirement"; exit 1; }
"$ROOT/bin/idea" verify "$NREQ" --pass
assert_out "an accepted non-converging plan did not reach complete" "initiative complete" "$ROOT/bin/idea" next
echo "an accepted non-converging plan reaches complete"

echo "=== NON-CONVERGING PLAN REVIEW PASS OK ==="

# ---------------------------------------------------------------------------
# PASS 6 — a change-set review that never converges.
#
# The third live-lock, and the one that kept the keyed magic path at 0/3 after
# the first two were fixed. `idea next` names `idea review CHG-1 approve`; that
# runs a fresh adversarial review, and a model reviewer is not a fixed point.
# Worse: the first attempt PERSISTS its findings, so every later attempt is
# refused by denyReviewOpenBlocking before the review even runs, and `idea apply`
# answers "needs approval first". Every other command refuses.
#
# No demo configuration could reach that state until this round: the change-set
# review shared the `review` schema key with the PRD review, so it could not be
# made to block on its own. It has its own key and its own knob now, and this is
# what asserts the escape.
#
# 50, not 1: on a converging queue the accept and a retry are indistinguishable.
# ---------------------------------------------------------------------------
echo "=== NON-CONVERGING CHANGE-SET REVIEW PASS ==="
CPROJ="$TMPROOT/csreview"
mkdir -p "$CPROJ"
cd "$CPROJ"
"$ROOT/bin/idea" init
# The queues are primed when the daemon opens the project and refilled on every
# config reload, so this restart must stay the LAST config write before `idea new`.
printf '\ndemo:\n  block_change_set_review_cycles: 50\n' >>.idea-harness/harness.yaml
kill "$DAEMON_PID" 2>/dev/null || true
wait "$DAEMON_PID" 2>/dev/null || true
"$ROOT/bin/idea" daemon >"$STATE/daemon.log" 2>&1 &
DAEMON_PID=$!
wait_for "daemon to report running (change-set review pass)" 5 daemon_running

"$ROOT/bin/idea" new "A parking permit renewal service"
wait_for "the PRD to converge" 60 initiative_left_running
"$ROOT/bin/idea" next --until-human
wait_for "a change set to exist" 60 chg1_in_changes

# `idea next` must name BOTH commands. Naming only the first is the defect: the
# one command it offered was refused and pointed at nothing that works.
CNEXT=$("$ROOT/bin/idea" next 2>&1 || true)
printf '%s\n' "$CNEXT"
printf '%s' "$CNEXT" | grep -q 'idea review CHG-1 approve --accept-findings' ||
  { echo "ERROR: idea next did not name the accept"; exit 1; }

# The plain approve is refused, and the refusal names the way out.
CERR=$("$ROOT/bin/idea" review CHG-1 approve 2>&1 || true)
printf '%s' "$CERR" | grep -q 'accept-findings' ||
  { echo "ERROR: the blocked approval does not name the accept: $CERR"; exit 1; }
echo "a blocked change-set review names its escape"

# And the accept finishes the loop.
"$ROOT/bin/idea" review CHG-1 approve --accept-findings
"$ROOT/bin/idea" apply CHG-1
wait_for "the accepted change set to read applied" 30 chg1_applied
CREQ=$("$ROOT/bin/idea" verify 2>&1 | grep -oE 'REQ-[0-9]+' | head -1 || true)
[ -n "$CREQ" ] || { echo "ERROR: could not find the active requirement"; exit 1; }
"$ROOT/bin/idea" verify "$CREQ" --pass
assert_out "an accepted change set did not reach complete" "initiative complete" "$ROOT/bin/idea" next
echo "an accepted change set reaches complete"

echo "=== NON-CONVERGING CHANGE-SET REVIEW PASS OK ==="
