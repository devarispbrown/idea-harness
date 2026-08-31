#!/usr/bin/env bash
set -euo pipefail

# Agent integration smoke: run a REAL Hermes coding task in the isolated
# container sandbox and assert it produces a real file diff (review B2/B3).
#
# Opt-in: requires a Hermes container image (with the `hermes` CLI installed)
# and a model + API key. These are repository variables/secrets, never CI-env
# defaults — so this tier is manual (workflow_dispatch), not run on every push.

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export GOPATH="$ROOT/.gopath" GOCACHE="$ROOT/.buildcache" GOMODCACHE="$ROOT/.gopath/pkg/mod"

HERMES_IMAGE="${HERMES_IMAGE:-}"
AGENT_MODEL="${AGENT_MODEL:-}"

if [ -z "$HERMES_IMAGE" ] || [ -z "$AGENT_MODEL" ]; then
  echo "SKIP: agent integration requires HERMES_IMAGE and AGENT_MODEL (plus a model API key)."
  echo "Set them as repository variables and the key as a secret, then trigger via workflow_dispatch."
  exit 0
fi

go build -o "$ROOT/bin/idea" "$ROOT/cmd/idea"

# Workspace-local temp dirs + an isolated daemon (mirrors scripts/smoke.sh).
TMPROOT="$ROOT/.smoke/agent-$$"
mkdir -p "$TMPROOT"
STATE="$TMPROOT/state"
PROJ="$TMPROOT/project"
mkdir -p "$STATE" "$PROJ"
export IDEA_HARNESS_STATE="$STATE"
export IDEA_HARNESS_ADDR="127.0.0.1:$((20000 + ($$ % 2000)))"

start_daemon() {
  "$ROOT/bin/idea" daemon >"$STATE/daemon.log" 2>&1 &
  echo $!
}

DPID=$(start_daemon)
trap 'kill "$DPID" 2>/dev/null || true' EXIT

for _ in $(seq 1 50); do
  if "$ROOT/bin/idea" daemon status 2>/dev/null | grep -q running; then break; fi
  sleep 0.1
done

cd "$PROJ"
"$ROOT/bin/idea" init >/dev/null

# Switch to the sandboxed Hermes agent (review B2) + a real model.
cat > "$PROJ/.idea-harness/harness.yaml" <<YAML
network: disabled
review:
    max_cycles: 3
providers: {}
executor:
    kind: hermes
    image: $HERMES_IMAGE
    model: $AGENT_MODEL
YAML

# Restart the daemon so the cached runtime re-reads harness.yaml.
kill "$DPID" 2>/dev/null || true
wait "$DPID" 2>/dev/null || true
sleep 0.5
DPID=$(start_daemon)

"$ROOT/bin/idea" new "Build a recipe sharing app" >/dev/null
sleep 4
"$ROOT/bin/idea" advance >/dev/null
sleep 4

# Snapshot the canonical workspace before the agent runs (exclude .idea-harness).
before=$(find "$PROJ" -type f -not -path '*/.idea-harness/*' | sort)

echo "=== EXECUTE (Hermes, sandboxed) ==="
"$ROOT/bin/idea" execute >/dev/null
sleep 12 # a real coding agent takes a while

echo "=== CHANGES ==="
"$ROOT/bin/idea" changes

"$ROOT/bin/idea" review CHG-1 approve >/dev/null
"$ROOT/bin/idea" apply CHG-1 >/dev/null
sleep 2

after=$(find "$PROJ" -type f -not -path '*/.idea-harness/*' | sort)
if [ "$before" = "$after" ]; then
  echo "FAIL: the Hermes agent produced no real file diff in the workspace"
  exit 1
fi
echo "OK: Hermes agent produced a real file diff"
