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
# Alternatively an Agent Client Protocol agent: ACP_IMAGE plus ACP_COMMAND, the
# agent's argv inside the image as a YAML flow sequence, e.g. [claude-code-acp]
# or [gemini, --experimental-acp]. ACP_KEY_ENV optionally renames the key
# inside the container (Gemini CLI reads GEMINI_API_KEY).
ACP_IMAGE="${ACP_IMAGE:-}"
ACP_COMMAND="${ACP_COMMAND:-}"
ACP_KEY_ENV="${ACP_KEY_ENV:-}"

if [ -n "$ACP_IMAGE" ] && [ -n "$ACP_COMMAND" ] && [ -n "$AGENT_MODEL" ]; then
  AGENT_KIND=acp
elif [ -n "$HERMES_IMAGE" ] && [ -n "$AGENT_MODEL" ]; then
  AGENT_KIND=hermes
else
  echo "SKIP: agent integration requires AGENT_MODEL and either HERMES_IMAGE or ACP_IMAGE + ACP_COMMAND (plus a model API key)."
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

# Switch to the sandboxed agent (review B2) + a real model.
if [ "$AGENT_KIND" = acp ]; then
  cat > "$PROJ/.idea-harness/harness.yaml" <<YAML
network: disabled
review:
    max_cycles: 3
providers: {}
executor:
    kind: acp
    image: $ACP_IMAGE
    model: $AGENT_MODEL
    acp:
        command: $ACP_COMMAND
${ACP_KEY_ENV:+        key_env: $ACP_KEY_ENV}
YAML
else
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
fi

# Restart the daemon so the cached runtime re-reads harness.yaml. The old
# daemon's listen socket may not release instantly, so retry the start until one
# binds (a fixed sleep raced it: "bind: address already in use").
kill "$DPID" 2>/dev/null || true
wait "$DPID" 2>/dev/null || true
for _ in $(seq 1 10); do
  DPID=$(start_daemon)
  sleep 1
  if "$ROOT/bin/idea" daemon status 2>/dev/null | grep -q running; then break; fi
done

"$ROOT/bin/idea" new "Create a file named hello.txt in the project root containing the exact text 'hello from idea-harness'" >/dev/null
"$ROOT/bin/idea" advance --wait >/dev/null

# Snapshot the canonical workspace before the agent runs (exclude .idea-harness).
before=$(find "$PROJ" -type f -not -path '*/.idea-harness/*' | sort)

echo "=== EXECUTE ($AGENT_KIND, sandboxed) ==="
"$ROOT/bin/idea" execute --wait >/dev/null

echo "=== CHANGES ==="
"$ROOT/bin/idea" changes
echo "=== CHANGE CHG-1 (diagnostic) ==="
"$ROOT/bin/idea" change CHG-1 --json

"$ROOT/bin/idea" review CHG-1 approve >/dev/null
"$ROOT/bin/idea" apply CHG-1 --wait

# apply is async; poll for the applied files to land rather than a fixed sleep.
after=""
for _ in $(seq 1 120); do
  after=$(find "$PROJ" -type f -not -path '*/.idea-harness/*' | sort)
  [ "$before" != "$after" ] && break
  sleep 1
done
if [ "$before" = "$after" ]; then
  echo "FAIL: the $AGENT_KIND agent produced no real file diff in the workspace"
  exit 1
fi
echo "OK: $AGENT_KIND agent produced a real file diff"
