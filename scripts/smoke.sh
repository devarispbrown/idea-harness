#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export GOPATH="$ROOT/.gopath" GOCACHE="$ROOT/.buildcache" GOMODCACHE="$ROOT/.gopath/pkg/mod"

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

# wait for health
for _ in $(seq 1 50); do
  if "$ROOT/bin/idea" daemon status 2>/dev/null | grep -q running; then break; fi
  sleep 0.1
done

cd "$PROJ"
"$ROOT/bin/idea" init
echo "--- NEW ---"
"$ROOT/bin/idea" new "Build a recipe sharing app"
echo "--- waiting for workflow ---"
sleep 3
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
sleep 3
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
sleep 3
echo "--- CHANGES ---"
"$ROOT/bin/idea" changes
echo "--- REVIEW approve CHG-1 ---"
"$ROOT/bin/idea" review CHG-1 approve
echo "--- APPLY CHG-1 ---"
"$ROOT/bin/idea" apply CHG-1
sleep 2
echo "--- WORKSPACE FILE (after apply) ---"
test -f "$PROJ/hello.txt" && echo "hello.txt present:" && cat "$PROJ/hello.txt"
echo "--- CHANGE CHG-1 ---"
"$ROOT/bin/idea" change CHG-1
echo "--- REVERT CHG-1 ---"
"$ROOT/bin/idea" revert CHG-1
sleep 1
echo "--- WORKSPACE FILE (after revert) ---"
if [ -f "$PROJ/hello.txt" ]; then echo "ERROR: hello.txt still present after revert"; exit 1; else echo "hello.txt removed"; fi
echo "--- CHANGE CHG-1 (after revert) ---"
"$ROOT/bin/idea" change CHG-1
echo "--- RISK ---"
"$ROOT/bin/idea" risk
echo "--- EXECUTORS ---"
"$ROOT/bin/idea" executors
