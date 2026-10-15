import { defineConfig, devices } from "@playwright/test";
import { e2ePort } from "./e2e/env";

// The browser suite (web/e2e): one hermetic demo daemon per run -- isolated
// state dir, non-default port, every provider key scrubbed -- seeded with an
// initiative, then driven through the whole loop in Chromium with an axe
// accessibility check on each page. `make e2e` builds the binary first;
// docs/ui.md "Browser tests" says how to run it by hand.
export default defineConfig({
  testDir: "./e2e",
  testMatch: /.*\.e2e\.ts$/,
  // One journey through one daemon: the steps share its state, in order.
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 120_000,
  expect: { timeout: 20_000 },
  reporter: process.env.CI ? [["list"], ["html", { open: "never", outputFolder: "e2e/.report" }]] : "list",
  outputDir: "e2e/.results",
  globalSetup: "./e2e/global-setup.ts",
  globalTeardown: "./e2e/global-teardown.ts",
  use: {
    baseURL: `http://127.0.0.1:${e2ePort()}`,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
