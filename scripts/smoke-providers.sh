#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export GOPATH="$ROOT/.gopath" GOCACHE="$ROOT/.buildcache" GOMODCACHE="$ROOT/.gopath/pkg/mod"

go build -o "$ROOT/bin/idea" "$ROOT/cmd/idea"

TMPROOT="$ROOT/.smoke/$$"
mkdir -p "$TMPROOT"
STATE="$TMPROOT/state"
PROJ="$TMPROOT/project"
mkdir -p "$STATE" "$PROJ"
export IDEA_HARNESS_STATE="$STATE"
export IDEA_HARNESS_ADDR="127.0.0.1:$((21000 + ($$ % 1000)))"

"$ROOT/bin/idea" daemon >"$STATE/daemon.log" 2>&1 &
DAEMON_PID=$!
trap 'kill "$DAEMON_PID" 2>/dev/null || true' EXIT

for _ in $(seq 1 50); do
  if "$ROOT/bin/idea" daemon status 2>/dev/null | grep -q running; then break; fi
  sleep 0.1
done

cd "$PROJ"
"$ROOT/bin/idea" init >/dev/null

echo "--- MODEL LIST (defaults) ---"
"$ROOT/bin/idea" model list

echo "--- MODEL SET ---"
"$ROOT/bin/idea" model set openrouter/anthropic/claude-sonnet-5

echo "--- harness.yaml ---"
cat .idea-harness/harness.yaml

echo "--- MODEL ADD (custom openai-compatible) ---"
"$ROOT/bin/idea" model add groq --type openai --base-url https://api.groq.com/openai/v1 --key-env GROQ_API_KEY

echo "--- MODEL LIST (after) ---"
"$ROOT/bin/idea" model list

echo "--- CONFIG ---"
"$ROOT/bin/idea" config
