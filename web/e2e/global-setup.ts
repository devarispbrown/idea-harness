import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
import { e2eDir, e2ePort, e2eWorkDir } from "./env";

// Starts the demo daemon and seeds one initiative (demo-daemon.sh), before any
// test runs. A failure here fails the run with the script's own output, which
// names the daemon log.
export default function globalSetup() {
  execFileSync("bash", [resolve(e2eDir, "demo-daemon.sh"), e2eWorkDir(), String(e2ePort())], {
    stdio: "inherit",
    timeout: 120_000,
  });
}
