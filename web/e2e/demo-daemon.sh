#!/usr/bin/env bash
# Starts a hermetic demo daemon for the Playwright suite (web/e2e) and seeds one
# initiative through the CLI, so the browser starts at a reviewed PRD.
#
# Hermetic on purpose: an isolated state dir, a non-default port, a scratch
# project directory, and every provider key unset -- the daemon falls back to
# the built-in deterministic demo provider and never reaches the network. A key
# leaking in from the developer's shell would turn the suite into a paid,
# nondeterministic run, which is why the keys are scrubbed rather than trusted.
#
# Usage: demo-daemon.sh <workdir> <port>
# Writes <workdir>/daemon.pid and <workdir>/env (state dir, address, project
# dir, initiative id) for the suite and for teardown, then exits 0 once the
# seeded initiative has left the running state.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="${1:?usage: demo-daemon.sh <workdir> <port>}"
PORT="${2:?usage: demo-daemon.sh <workdir> <port>}"
IDEA="${IDEA_BIN:-$ROOT/bin/idea}"

[ -x "$IDEA" ] || { echo "demo-daemon: $IDEA is not built; run 'make build' first" >&2; exit 1; }

# Every *_API_KEY the provider catalog reads, plus OLLAMA_HOST (a reachable
# local Ollama would otherwise be picked up as a real provider).
while IFS='=' read -r name _; do
  case "$name" in *_API_KEY | OLLAMA_HOST) unset "$name" ;; esac
done < <(env)

rm -rf "$WORK"
mkdir -p "$WORK/state" "$WORK/project"
export IDEA_HARNESS_STATE="$WORK/state"
export IDEA_HARNESS_ADDR="127.0.0.1:$PORT"
# remote.test is how remote.e2e.ts reaches this loopback daemon as a browser
# on another device would: Chromium maps the name to 127.0.0.1, so the page's
# Host is not loopback, it is served without the token, and it signs in with a
# pairing code. Naming it here is what the Host/Origin guard requires.
printf 'daemon:\n  allowed_hosts: [remote.test]\n' >"$WORK/state/config.yaml"

"$IDEA" daemon >"$WORK/state/daemon.log" 2>&1 &
echo $! >"$WORK/daemon.pid"

deadline=$((SECONDS + 15))
until "$IDEA" daemon status 2>/dev/null | grep -q running; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "demo-daemon: daemon did not start" >&2
    cat "$WORK/state/daemon.log" >&2
    exit 1
  fi
  sleep 0.1
done

cd "$WORK/project"
# Its own repository, and not optional: the work dir sits inside this repo
# under web/e2e/.work, which web/e2e/.gitignore ignores. Without a repository
# boundary here, the daemon's after-run snapshot honours the OUTER repo's
# ignore rules, ignores every file the agent writes, and every change set
# comes out a no-op.
git init -q
"$IDEA" init >/dev/null
# `idea new` waits for M1 (framing -> research -> PRD -> review) by default.
"$IDEA" new "A recipe sharing app for home cooks" >/dev/null
# One grounding document, so Explore has a row whose "trace" link the suite
# can follow.
printf '# Interview notes\n\nHome cooks want to share family recipes with relatives.\n' >"$WORK/interview-notes.md"
"$IDEA" docs add "$WORK/interview-notes.md" --kind market --title "Interview notes" >/dev/null

cat >"$WORK/env" <<ENV
IDEA_HARNESS_STATE=$WORK/state
IDEA_HARNESS_ADDR=$IDEA_HARNESS_ADDR
PROJECT_DIR=$WORK/project
ENV
echo "demo daemon on http://$IDEA_HARNESS_ADDR/"
