# Milestone 8 — One-command coding-agent setup (Hermes default, OMP backup)

Close the code-generation cliff: turn "wire a real coding agent" into a single
command. Hermes is the default; OMP is the fallback.

## Scope

### A. `idea agent setup` CLI (new)
`idea agent setup [--kind hermes|omp] --image <img> --model <provider/model>`

1. `--kind` defaults to `hermes`.
2. Requires `--image` (container image containing the agent CLI) and `--model`
   (e.g. `anthropic/claude-…`). Reads `HERMES_IMAGE`/`AGENT_MODEL` env as defaults
   if flags omitted (mirrors scripts/agent-smoke.sh).
3. **Backup behavior:** before writing config, verify the agent CLI is present in
   the image (`docker run --rm <img> <hermes|omp> --version`). If the requested
   agent's CLI is missing but the OTHER agent's CLI is present, auto-fall back to
   the other with a notice ("hermes not found in image; falling back to omp").
4. Write `executor.kind`/`executor.image`/`executor.model` to `.idea-harness/harness.yaml`
   via the daemon, reload the runtime, and print the resulting `idea executors`
   selection.

### B. Daemon: write executor config
Extend `handleUpdateProviders` to accept an `executor` object (`kind`/`image`/`model`)
and write it via `config.WriteHarness` + `ReloadProject` (same path as providers).
This is the only missing write surface (executor config is currently YAML-only).

### C. Runtime launch error → actionable fallback hint
When a sandboxed agent fails to launch (image/CLI missing at `Start`), the error
message already names the agent; add "run `idea agent setup --kind omp …`" as the
suggested fallback.

### D. Docs
- `docs/agents.md`: add a "Build the image" section with a Dockerfile template for
  Hermes and OMP (honest: install step depends on upstream; provide a best-effort
  template + note that a prebuilt image is also accepted).
- `README.md`: replace the "wire OMP/Hermes" sentence with the one-command
  `idea agent setup --image … --model …` quickstart.

## Out of scope
- Pulling/building an official image (none exists; the user provides or builds it).
- Runtime auto-fallback (config is explicit after setup; the CLI verifies at setup).
- A UI executor-config surface (CLI-only this milestone, consistent with Milestone 7).

## Definition of done
- A–D implemented; `go vet ./...`, `go test ./...`, `scripts/smoke.sh` green.
- A unit test for the CLI arg parsing / fallback logic (no Docker required).
- Plan reviewed (adversarial) before implementation; commits reviewed after.

---

## Plan review corrections (v2)

Four-perspective review (Product, UX/API, Security, Reliability): the auto-fallback
and bare `docker run` verification are unsafe; naming and the endpoint are wrong.

- **Command**: `idea executors setup --kind hermes|omp --image <img> [--model <spec>] [--yes]`
  (NOT `idea agent`). Aligns with `idea executors`/`executor.kind`.
- **Endpoint**: dedicated `PUT /api/v1/projects/{projectId}/executors` (NOT overload
  providers). Validates `kind ∈ {internal,container,omp,hermes}`; **field-level merge**
  (only overwrite supplied fields); load config from disk (not the stale cache) before
  overlay; write + reload.
- **No silent auto-fallback.** OMP is the opt-in `--kind omp` alternative, never a
  silent switch. If the chosen CLI is absent, error and suggest `--kind omp`.
- **Security**: verification `docker run` uses the hardened flags (`--network none
  --read-only --cap-drop ALL --no-new-privileges --user --memory --pids-limit
  --tmpfs /tmp --entrypoint ""`), argv-built (no shell interpolation). Print the
  network-egress + auto-approve + key-forwarding tradeoff and require `--yes`.
  Verification is UX-only (trust comes from the image source; recommend a pinned digest).
- **Reliability**: pre-flight `exec.LookPath("docker")` + `docker image inspect`
  (no pull); branch exit 127 (CLI missing) vs 125 (daemon/pull); timeout the probe;
  surface "config written but not applied (N ops running)" distinctly.
- **Honest framing**: "one-command wiring for an image you already have"; image build
  stays out of scope. `--model` optional (falls back to the default-model chain).
