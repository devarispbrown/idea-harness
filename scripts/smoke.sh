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

daemon_running() { "$ROOT/bin/idea" daemon status 2>/dev/null | grep -q running; }
# The two predicates below are negative ("the output no longer says X"), so they
# must first establish that there IS output: a command that errored or printed
# nothing would otherwise satisfy them on the first 0.1s poll and let the script
# race ahead of the daemon — something the fixed sleeps they replaced could not do.
initiative_left_running() {
  local out
  out=$("$ROOT/bin/idea" status 2>/dev/null) || return 1
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

# wait for health
wait_for "daemon to report running" 5 daemon_running

cd "$PROJ"
# Make the workspace a Git repo so the opt-in git-commit-on-apply path is
# exercised end-to-end (not just the byte-for-byte file revert).
git init -q
git config user.name "idea-harness-smoke"
git config user.email "smoke@example.invalid"
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
"$ROOT/bin/idea" execute
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
