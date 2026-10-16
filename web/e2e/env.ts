import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

// This directory (ESM has no __dirname; web/package.json is "type": "module").
export const e2eDir = fileURLToPath(new URL(".", import.meta.url));

// Where the suite's daemon lives. Not the default 8787, so a developer's own
// daemon is never touched; overridable for a machine where 19590 is taken.
export function e2ePort(): number {
  return Number(process.env.IDEA_E2E_PORT || 19590);
}

// The daemon's state dir and the scratch project, inside web/e2e (ignored by
// git) rather than the OS temp dir: some sandboxes only let the daemon write
// inside the workspace, the same reason scripts/smoke.sh keeps its own.
export function e2eWorkDir(): string {
  return resolve(e2eDir, ".work");
}
