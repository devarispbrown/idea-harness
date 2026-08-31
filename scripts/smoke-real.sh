#!/usr/bin/env bash
# End-to-end run against a REAL model provider.
#
# Usage:
#   export OPENAI_API_KEY=...   # or ANTHROPIC_API_KEY / GOOGLE_API_KEY / OPENROUTER_API_KEY
#   IDEA_REAL_MODEL="openai/gpt-4o-mini" bash scripts/smoke-real.sh
#
# The provider is chosen from the default_model prefix (e.g. "openai/…").
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export GOPATH="$ROOT/.gopath" GOCACHE="$ROOT/.buildcache" GOMODCACHE="$ROOT/.gopath/pkg/mod"

: "${IDEA_REAL_MODEL:?set IDEA_REAL_MODEL, e.g. openai/gpt-4o-mini}"

go build -o "$ROOT/bin/idea" "$ROOT/cmd/idea"

TMPROOT="$ROOT/.smoke/real-$$"
mkdir -p "$TMPROOT"
STATE="$TMPROOT/state"
PROJ="$TMPROOT/project"
mkdir -p "$STATE" "$PROJ"
export IDEA_HARNESS_STATE="$STATE"
export IDEA_HARNESS_ADDR="127.0.0.1:$((22000 + ($$ % 1000)))"

"$ROOT/bin/idea" daemon >"$STATE/daemon.log" 2>&1 &
DAEMON_PID=$!
trap 'kill "$DAEMON_PID" 2>/dev/null || true' EXIT

for _ in $(seq 1 50); do
  if "$ROOT/bin/idea" daemon status 2>/dev/null | grep -q running; then break; fi
  sleep 0.1
done

cd "$PROJ"
"$ROOT/bin/idea" init >/dev/null
"$ROOT/bin/idea" model set "$IDEA_REAL_MODEL"
"$ROOT/bin/idea" new "Build a recipe sharing app" 
echo "--- waiting for the real model workflow (may take a minute) ---"
sleep 30
"$ROOT/bin/idea" status
"$ROOT/bin/idea" explain
echo "--- PRD ---"
"$ROOT/bin/idea" prd 2>&1 | head -40
