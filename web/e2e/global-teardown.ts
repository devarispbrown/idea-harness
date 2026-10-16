import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { e2eWorkDir } from "./env";

// Stops the suite's daemon. Its state is left in web/e2e/.work for a failed
// run's post-mortem (daemon.log is in state/); the next run starts clean.
export default function globalTeardown() {
  const pidFile = join(e2eWorkDir(), "daemon.pid");
  if (!existsSync(pidFile)) return;
  const pid = Number(readFileSync(pidFile, "utf8").trim());
  if (!pid) return;
  try {
    process.kill(pid, "SIGTERM");
  } catch {
    // Already gone.
  }
}
