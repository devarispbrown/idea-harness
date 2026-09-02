# Milestone 8b — Auto-download the default coding agent

The `idea executors setup` DX still requires the user to know an image name.
Fix: auto-download a pinned, known-good Hermes image; OMP becomes explicit.

## Design

- **A.** `executor.DefaultAgentImage(kind string) string`: returns a pinned
  Hermes image (`nousresearch/hermes-agent:<pinned-tag>`) for `hermes`, `""` for
  `omp` (no canonical OMP image).
- **B.** `executor.PullImage(ctx, image)`: `docker pull <image>` and return the
  pulled digest so the CLI can display exactly what third-party code is now trusted.
- **C.** `idea executors setup`: `--image` is now **optional** for `hermes` —
  when omitted, use the default, auto-`docker pull`, verify the `hermes` CLI, and
  print the pulled digest + the security tradeoff (still requires `--yes`).
  For `omp`, `--image` stays **required** ("no canonical OMP image; provide one
  you trust"). No silent fallback.
- **D.** `docs/agents.md` + `README.md`: Hermes auto-download as the primary path;
  OMP documented as bring-your-own-image.
- **E.** Test the image-resolution logic (default vs required) without Docker.

## Trust note (honest)
Auto-download runs third-party code (Nous Research's Hermes). We pin a versioned
tag (not `latest`), show the pulled digest, and keep the `--yes` confirmation.
The `hermes --in /work --yolo chat --query … --model …` invocation is validated
against the pinned version by `scripts/agent-smoke.sh` (opt-in).
