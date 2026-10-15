import { readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { expectAccessible } from "./a11y";
import { e2eWorkDir } from "./env";

// UI Phase 4 -- settings and automations -- against the same hermetic demo
// daemon the journey uses (it runs after journey.e2e.ts, and leaves the
// project's settings as it found them): edit an integration, switch the
// executor profile to internal and back, watch a spending cap stop a run on
// the demo provider and raising it send the run back, and turn an automation
// on, run it now, and see its history.
test.describe.configure({ mode: "serial" });

let page: Page;

function projectDir(): string {
  const env = readFileSync(resolve(e2eWorkDir(), "env"), "utf8");
  return env.match(/^PROJECT_DIR=(.*)$/m)![1];
}
const harnessPath = () => resolve(projectDir(), ".idea-harness", "harness.yaml");

test.beforeAll(async ({ browser }, testInfo) => {
  const context = await browser.newContext({ baseURL: testInfo.project.use.baseURL });
  page = await context.newPage();
});
test.afterAll(async () => {
  await page.context().close();
});

test("settings: an integration is configured and turned off again, token names only", async () => {
  await page.goto("/#/settings");
  const linear = page.locator('[data-integration="linear"]');
  await linear.getByRole("button", { name: "Set up Linear" }).click();
  await linear.getByLabel("Team key").fill("ENG");
  await linear.getByLabel("Poll interval (seconds, at least 15)").fill("5");
  await linear.getByRole("button", { name: "Turn on Linear" }).click();
  // Refused at the field, before a round trip, in the loader's own terms.
  await expect(linear.locator("p.error", { hasText: "at least 15" })).toBeVisible();
  await linear.getByLabel("Poll interval (seconds, at least 15)").fill("120");
  await linear.getByLabel("Done").fill("Shipped");
  await expect(linear.locator(".integ-editor")).toContainText("LINEAR_API_KEY is not set");
  await expectAccessible(page, "settings (linear editor)");
  await linear.getByRole("button", { name: "Turn on Linear" }).click();
  await expect(page.getByText("Linear settings saved to harness.yaml.")).toBeVisible();
  // Written to the file, as names and values a person would write.
  const yaml = readFileSync(harnessPath(), "utf8");
  expect(yaml).toContain("team_key: ENG");
  expect(yaml).toContain("poll_interval_seconds: 120");
  expect(yaml).toContain("done: Shipped");
  await expect(linear).toContainText("Configured but not running");

  await linear.getByRole("button", { name: "Edit settings" }).click();
  await expect(linear.getByLabel("Team key")).toHaveValue("ENG");
  await linear.getByRole("button", { name: "Turn off" }).click();
  await expect(page.getByText(/Linear turned off/)).toBeVisible();
  expect(readFileSync(harnessPath(), "utf8")).not.toContain("team_key");
});

test("settings: the executor profile switches to internal and back to auto", async () => {
  await page.goto("/#/settings");
  const kind = page.locator("#executor-kind");
  await expect(kind).toHaveValue("auto");
  await kind.selectOption("internal");
  await expect(page.getByTestId("executor-trust")).toContainText("Nothing leaves this machine");
  await page.getByRole("button", { name: "Save executor" }).click();
  await expect(page.getByText("Executor saved.")).toBeVisible();
  await page.reload();
  await expect(page.locator("#executor-kind")).toHaveValue("internal");
  expect(readFileSync(harnessPath(), "utf8")).toMatch(/kind: internal/);

  // acp shows its own fields and its trade-off, and refuses a save without
  // what it needs -- nothing is written.
  await page.locator("#executor-kind").selectOption("acp");
  await expect(page.getByTestId("executor-trust")).toContainText("MCP servers");
  await expect(page.getByTestId("mcp-servers")).toBeVisible();
  await page.getByRole("button", { name: "Save executor" }).click();
  await expect(page.getByText(/acp needs an image/)).toBeVisible();
  await expectAccessible(page, "settings (acp profile)");

  await page.locator("#executor-kind").selectOption("auto");
  await page.getByRole("button", { name: "Save executor" }).click();
  await expect(page.getByText("Executor saved.")).toBeVisible();
  await page.reload();
  await expect(page.locator("#executor-kind")).toHaveValue("auto");
  await expect(page.locator(".executor-resolution")).toContainText("resolves to: internal");
});

test("budget: a cap stops a demo run, and raising it sends the initiative back", async () => {
  // Make the demo provider charge for its calls, so a cap can be reached
  // with no real provider. The daemon picks the edit up on the next request.
  const original = readFileSync(harnessPath(), "utf8");
  writeFileSync(harnessPath(), original + "demo:\n  cost_per_call_usd: 0.1\n");
  try {
    await page.goto("/#/settings");
    await page.getByLabel("Default cap per initiative (USD, empty for none)").fill("0.15");
    await page.getByRole("button", { name: "Save default" }).click();
    await expect(page.getByText(/capped at \$0\.15/)).toBeVisible();

    await page.goto("/");
    await page.getByPlaceholder("What are you thinking about building?").fill("A packing list app for weekend trips");
    await page.getByPlaceholder("What are you thinking about building?").press("Enter");
    await expect(page).toHaveURL(/#\/p\/[0-9a-f]{32}\/i\/[0-9a-f]{32}\/explore$/);
    const id = page.url().match(/\/i\/([0-9a-f]{32})\//)![1];

    // The run stops before the call that would go over: the shell meter on
    // every phase says so and links to where the cap is raised.
    const shell = page.locator(".budget-shell");
    await expect(shell).toContainText("Stopped at its spending cap", { timeout: 60_000 });
    await expect(shell).toContainText("of $0.15 (project default)");
    await expectAccessible(page, "explore (stopped at the cap)");

    await shell.getByRole("link", { name: "Raise the cap" }).click();
    await expect(page).toHaveURL(new RegExp(`/i/${id}/build$`));
    const card = page.locator(".budget-card");
    await expect(card).toContainText("reached its cap");
    await card.getByLabel("This initiative's cap (USD)").fill("0");
    await card.getByRole("button", { name: "Raise cap" }).click();
    await expect(card.getByText(/Must be more than \$0/)).toBeVisible();
    await card.getByLabel("This initiative's cap (USD)").fill("10");
    await card.getByRole("button", { name: "Raise cap" }).click();
    await expect(card.getByText(/back where it stopped/)).toBeVisible();
    await expect(card).toContainText("of $10.00 (this initiative's cap)");
    await expectAccessible(page, "build (budget raised)");
  } finally {
    // Leave the project as it was for anything after this file.
    await page.goto("/#/settings");
    const remove = page.getByRole("button", { name: "Remove default" });
    if (await remove.isVisible()) await remove.click();
    await expect(page.getByText(/Project default removed/)).toBeVisible();
    const now = readFileSync(harnessPath(), "utf8");
    writeFileSync(harnessPath(), now.replace(/\ndemo:\n\s+cost_per_call_usd: 0\.1\n?/, "\n"));
  }
});

test("automations: every job is off until enabled; toggle, run now and history", async () => {
  await page.goto("/");
  await page.getByRole("button", { name: "Automations" }).click();
  await expect(page).toHaveURL(/#\/p\/[0-9a-f]{32}\/automations$/);
  await expect(page.getByRole("heading", { name: "Automations", level: 1 })).toBeVisible();
  for (const name of ["linear_inbound", "metric_measurement", "eval"]) {
    await expect(page.locator(`[data-automation="${name}"]`).getByRole("switch")).not.toBeChecked();
  }
  await expect(page.getByTestId("next-metric_measurement")).toContainText("Off");
  await expectAccessible(page, "automations");

  // Metric measurement: on, then off again.
  const metrics = page.locator('[data-automation="metric_measurement"]');
  await metrics.getByRole("switch").check();
  await expect(metrics.getByRole("status")).toContainText("is on");
  await expect(metrics.getByRole("switch")).toBeChecked();
  expect(readFileSync(harnessPath(), "utf8")).toMatch(/metric_measurement:\n\s+enabled: true/);
  // Nothing to measure in this project: Run now says why.
  await metrics.getByRole("button", { name: "Run now" }).click();
  await expect(metrics.getByRole("status")).toContainText("command or url source");
  await metrics.getByRole("switch").uncheck();
  await expect(metrics.getByRole("switch")).not.toBeChecked();

  // The eval: refused without a model and a cap; set both, run it now. The
  // suite has no provider key, so the run fails at once -- before any spend --
  // and the history records it, with its cost and why.
  const ev = page.locator('[data-automation="eval"]');
  await ev.getByRole("button", { name: "Save eval settings" }).click();
  await expect(ev.getByText(/provider\/model/)).toBeVisible();
  await ev.getByLabel("Model").fill("openai/gpt-4o-mini");
  await ev.getByLabel("Cost cap per run (USD)").fill("1");
  await ev.getByRole("button", { name: "Save eval settings" }).click();
  await expect(ev.getByRole("status")).toContainText("Eval settings saved.");
  await ev.getByRole("button", { name: "Run now" }).click();
  const row = ev.locator(".automation-history tbody tr").first();
  await expect(row).toContainText("failed", { timeout: 30_000 });
  await expect(row).toContainText("OPENAI_API_KEY");
  await expect(row).toContainText("Run now");
  await expect(ev.getByRole("switch")).not.toBeChecked();
  await expectAccessible(page, "automations (after a run)");
});
