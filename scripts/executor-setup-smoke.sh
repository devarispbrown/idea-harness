#!/usr/bin/env bash
set -euo pipefail

# Executor-setup smoke: run `idea executors setup --smoke` against the PINNED
# Hermes image (auto-downloaded), asserting the CLI contract produces a real
# file change. Opt-in via workflow_dispatch: requires AGENT_MODEL (repo var)
# and the model's API key (secret).
#
# Unlike scripts/agent-smoke.sh (which exercises the full pipeline), this runs
# the direct executor round-trip and validates the auto-download path.

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export GOPATH="$ROOT/.gopath" GOCACHE="$ROOT/.buildcache" GOMODCACHE="$ROOT/.gopath/pkg/mod"

AGENT_MODEL="${AGENT_MODEL:-}"
if [ -z "$AGENT_MODEL" ]; then
  echo "SKIP: executor-setup-smoke requires AGENT_MODEL (plus the model's API key)."
  exit 0
fi

go build -o "$ROOT/bin/idea" "$ROOT/cmd/idea"

TMPROOT="$ROOT/.smoke/exec-setup-$$"
mkdir -p "$TMPROOT"
STATE="$TMPROOT/state"
PROJ="$TMPROOT/project"
mkdir -p "$STATE" "$PROJ"
export IDEA_HARNESS_STATE="$STATE"
export IDEA_HARNESS_ADDR="127.0.0.1:$((21000 + ($$ % 2000)))"
# `idea init` auto-starts a daemon on this throwaway state; stop it on exit, or
# every run leaves one behind (six accumulated in one afternoon).
trap '"$ROOT/bin/idea" daemon stop >/dev/null 2>&1 || true; rm -rf "$TMPROOT"' EXIT

cd "$PROJ"
"$ROOT/bin/idea" init >/dev/null

echo "=== executors setup --smoke (pinned Hermes image) ==="
"$ROOT/bin/idea" executors setup --model "$AGENT_MODEL" --yes --smoke
