import { expect, test, type Page } from "@playwright/test";
import { expectAccessible } from "./a11y";

// The whole loop through the browser, on the deterministic demo provider:
// idea -> PRD -> answer a question -> plan (and a no-op revision) -> run the
// task with a note -> diff -> request changes with a note -> rerun -> approve
// -> apply -> verify -> activity/health, with an axe check on every page.
//
// One serial journey, because every step acts on the state the last one left;
// global-setup.ts seeded the initiative ("A recipe sharing app for home
// cooks") through the CLI, so the browser starts at a reviewed PRD.
test.describe.configure({ mode: "serial" });

let page: Page;
let initiative = "";
const at = (path: string) => `/#/i/${initiative}/${path}`;

test.beforeAll(async ({ browser }, testInfo) => {
  // A context of our own (axe needs one), kept for the whole journey.
  const context = await browser.newContext({ baseURL: testInfo.project.use.baseURL });
  page = await context.newPage();
});
test.afterAll(async () => {
  await page.context().close();
});

test("home lists the seeded initiative", async () => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Initiatives" })).toBeVisible();
  // The project's own name, not a hard-coded label.
  await expect(page.locator(".ph-project")).toHaveText("project");
  const card = page.getByText("A recipe sharing app for home cooks").first();
  await expect(card).toBeVisible();
  await expectAccessible(page, "home");
  await card.click();
  // The shell names the project in every address (#/p/<project>/…).
  await expect(page).toHaveURL(/#\/p\/[0-9a-f]{32}\/i\/[0-9a-f]{32}\/explore$/);
  initiative = page.url().match(/\/i\/([0-9a-f]{32})\//)![1];
  // One demo banner, worded as what demo means: no model selected.
  await expect(page.locator(".banner-app")).toHaveCount(1);
  await expect(page.locator(".banner-app")).toContainText("no model is selected");
});

test("explore shows the brief and research", async () => {
  await page.goto(at("explore"));
  await expect(page.getByRole("heading", { name: "Explore", exact: false }).first()).toBeVisible();
  await expectAccessible(page, "explore");
});

test("define: the PRD is approved, a question is answered and stays visible", async () => {
  await page.goto(at("define"));
  await expect(page.getByText("Product requirements · v1")).toBeVisible();
  // Only questions are open: "accept all" has nothing to do, and says so
  // instead of offering a button that silently does nothing.
  await expect(page.getByRole("button", { name: "Accept all recommendations" })).toHaveCount(0);
  await expect(page.getByText("Nothing to accept automatically")).toBeVisible();
  await expectAccessible(page, "define");

  const answer = page.getByLabel("Answer to: Who is the primary user?");
  await answer.fill("Home cooks who share family recipes");
  await page.getByRole("button", { name: "Answer", exact: true }).click();
  // Answered questions move to Resolved, with their answer, instead of
  // vanishing.
  await expect(page.getByRole("heading", { name: "Resolved" })).toBeVisible();
  await expect(page.getByText("Home cooks who share family recipes")).toBeVisible();
});

test("design and plan: advance, then a revision that changes nothing says so", async () => {
  await page.goto(at("define"));
  await page.getByRole("button", { name: "Proceed to design & plan →" }).click();
  await page.goto(at("plan"));
  await expect(page.getByText("TASK-1", { exact: true })).toBeVisible({ timeout: 60_000 });
  await expect(page.getByText("Execution plan · v1")).toBeVisible();
  // The demo's plan review passes on its own; a parked plan is approved here.
  const approve = page.getByRole("button", { name: "Approve plan →" });
  if (await approve.isVisible()) await approve.click();
  await expect(page.locator(".phase-pill", { hasText: /approved/i })).toBeVisible();
  await expectAccessible(page, "plan");

  await page.getByRole("button", { name: "Ask for a revision…" }).click();
  await page.getByLabel("What should the revised plan change?").fill("Keep the plan as one vertical slice");
  await page.getByRole("button", { name: "Regenerate the plan" }).click();
  // The page's own status line (the app-wide live region is a status too).
  await expect(page.locator(".revision-status[role=status]")).toContainText("same plan: still v1, tasks unchanged", {
    timeout: 60_000,
  });
  await expect(page.getByText("TASK-1", { exact: true })).toBeVisible();
  await expect(page.getByText("TASK-2")).toHaveCount(0);
  await expect(page.getByText(/Diff v1 → v1/)).toHaveCount(0);
});

test("build: run with a note, view the diff, request changes with a note", async () => {
  await page.goto(at("build"));
  const note = page.getByLabel(/Note to the agent for/);
  await expect(note).toBeVisible();
  await note.fill("Keep the first slice small");
  await page.getByRole("button", { name: /^Run TASK-1/ }).click();
  const card = page.locator(".cs-card", { hasText: "CHG-1" });
  await expect(card.locator(".cs-pill")).toHaveText(/review required/i, { timeout: 60_000 });
  // The transient "Starting…" line is gone once the change set exists.
  await expect(page.getByText(/^Starting TASK-1…$/)).toHaveCount(0);
  // Only the passing checks are on it: nothing to "accept".
  await expect(card.getByRole("button", { name: /Approve, accepting/ })).toHaveCount(0);
  await expectAccessible(page, "build (review required)");

  await card.getByRole("button", { name: "View diff" }).click();
  await expect(card.locator(".cs-files")).toBeVisible();
  await expect(card.locator(".cs-file-path").first()).toBeVisible();

  // The note form is a disclosure: it takes focus, Escape closes it and
  // hands focus back to the button, and Ctrl+Enter sends it.
  const requestButton = card.getByRole("button", { name: "Request changes…" });
  await requestButton.click();
  const changeNote = card.getByLabel("What should change in CHG-1?");
  await expect(changeNote).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(changeNote).toHaveCount(0);
  await expect(requestButton).toBeFocused();
  await requestButton.click();
  await changeNote.fill("Name the file after the feature");
  await expectAccessible(page, "build (request changes form)");
  await changeNote.press("Control+Enter");
  await expect(card.locator(".cs-pill")).toHaveText(/changes requested/i);
  await expect(page.getByTestId("live-announcer")).toContainText("CHG-1 sent back with your note");
  await expect(card).toContainText("Name the file after the feature");
});

test("build: the rerun is approved and applied; the chip says applied, demo beside it", async () => {
  await page.goto(at("build"));
  await page.getByRole("button", { name: /^Run TASK-1/ }).click();
  const card = page.locator(".cs-card", { hasText: "CHG-2" });
  await expect(card.locator(".cs-pill")).toHaveText(/review required/i, { timeout: 60_000 });
  await card.getByRole("button", { name: "Approve", exact: true }).click();
  await expect(card.locator(".cs-pill")).toHaveText(/approved/i);
  await card.getByRole("button", { name: "Apply" }).click();
  await expect(card.locator(".cs-pill")).toHaveText(/^applied$/i, { timeout: 60_000 });
  await expect(card.locator(".cs-pill-marker")).toHaveText(/demo/i);
  await expectAccessible(page, "build (applied)");
});

test("plan stays approved, with its bar, once building has started — on a phone too", async () => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(at("plan"));
  await expect(page.locator(".phase-pill", { hasText: /approved/i })).toBeVisible();
  await expect(page.locator(".commit-bar")).toContainText("Build has started");
  await expectAccessible(page, "plan (mobile)");
  await page.setViewportSize({ width: 1280, height: 900 });
});

test("verify: pass with evidence", async () => {
  await page.goto(at("verify"));
  await expect(page.getByText("Pass and Fail appear once")).toHaveCount(0);
  await expectAccessible(page, "verify");
  await page.getByLabel("Evidence note for REQ-1").fill("Opened the app and shared a recipe");
  await page.getByRole("button", { name: "Pass", exact: true }).click();
  await expect(page.locator(".verify-card .phase-pill", { hasText: "verified" })).toBeVisible();
});

test("activity is the timeline; health shows the graph and its gaps", async () => {
  await page.goto(at("verify"));
  await page.getByRole("button", { name: "Activity ↗" }).click();
  await expect(page).toHaveURL(new RegExp(`/i/${initiative}/changed$`));
  await expectAccessible(page, "activity (what changed)");
  await page.getByRole("button", { name: "Health ↗" }).click();
  await expect(page).toHaveURL(new RegExp(`/i/${initiative}/health$`));
  await expect(page.getByText(/Product gaps/)).toBeVisible();
  await expectAccessible(page, "health");
});

test("explore's trace opens the lineage route for that document", async () => {
  await page.goto(at("explore"));
  const trace = page.getByRole("button", { name: /^Trace / }).first();
  if ((await trace.count()) === 0) test.skip(true, "the demo seeds no context documents");
  await trace.click();
  await expect(page).toHaveURL(new RegExp(`/i/${initiative}/lineage/context_doc/`));
});

test("product graph views: gaps, outcomes, quality and a task's lineage", async () => {
  await page.goto(at("gaps"));
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expectAccessible(page, "gaps");
  await page.goto(at("outcomes"));
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expectAccessible(page, "outcomes");
  await page.goto(at("quality"));
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expectAccessible(page, "quality");
  // The applied task's lineage: reached by code, the way every Why / Impact
  // panel links to it.
  await page.goto(at("lineage/task/TASK-1"));
  await expect(page.getByRole("heading", { level: 1, name: /TASK-1/ })).toBeVisible();
  await expect(page.getByRole("heading", { name: /^Why/ })).toBeVisible();
  await expect(page.getByRole("heading", { name: /^Impact/ })).toBeVisible();
  await expectAccessible(page, "lineage (task)");
});

test("the queue, the design page and the document reader pass axe too", async () => {
  await page.goto("/#/queue");
  await expect(page.getByRole("heading", { level: 1, name: "Needs you" })).toBeVisible();
  await expectAccessible(page, "queue");
  await page.goto(at("design"));
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expectAccessible(page, "design");
  await page.goto(at("define"));
  await page.getByRole("button", { name: /^Read/ }).first().click();
  await expect(page).toHaveURL(new RegExp(`/i/${initiative}/doc/`));
  await expect(page.getByRole("heading").first()).toBeVisible();
  await expectAccessible(page, "document reader");
});

test("a decision id that does not exist renders not-found, with no Send box", async () => {
  await page.goto(at("decision/not-a-decision"));
  await expect(page.getByRole("heading", { name: "Decision not found" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Send" })).toHaveCount(0);
  await expectAccessible(page, "decision not found");
  await page.goto("/#/i/0123456789abcdef0123456789abcdef/health");
  await expect(page.getByRole("heading", { name: "Initiative not found" })).toBeVisible();
});

test("keyboard: skip link, focus follows the route, ⌘K finds a node code, ? lists shortcuts", async () => {
  await page.goto(at("define"));
  // A fresh load, so nothing has focus yet: the first Tab stop is Skip to
  // content, and it lands in the page.
  await page.reload();
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await page.keyboard.press("Tab");
  const skip = page.getByRole("link", { name: "Skip to content" });
  await expect(skip).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.locator("#main-content")).toBeFocused();

  // ⌘K / Ctrl+K: a typed code opens that node's lineage, and focus moves to
  // the new page's heading rather than staying on a control that is gone.
  await page.keyboard.press("ControlOrMeta+k");
  const palette = page.getByRole("dialog", { name: "Command palette" });
  await expect(palette).toBeVisible();
  await expect(page.getByRole("combobox")).toBeFocused();
  await expectAccessible(page, "command palette", { include: ".cmd-panel" });
  await page.keyboard.type("task-1");
  await expect(palette.getByRole("option").first()).toContainText("Lineage of TASK-1");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(new RegExp(`/i/${initiative}/lineage/task/TASK-1$`));
  await expect(page.getByRole("heading", { level: 1, name: /TASK-1/ })).toBeFocused();

  // Settings sections are palette entries too, and focus lands on the section.
  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.getByRole("combobox")).toBeFocused();
  await page.keyboard.type("executor");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/#\/p\/[0-9a-f]{32}\/settings$/);
  await expect(page.getByRole("heading", { level: 2, name: "Executor" })).toBeFocused();

  // ? opens the reference as a modal; Escape closes it and focus comes back.
  await page.goto(at("plan"));
  const heading = page.getByRole("heading", { level: 1 });
  await expect(heading).toBeFocused();
  await page.keyboard.press("?");
  const help = page.getByRole("dialog", { name: "Keyboard shortcuts" });
  await expect(help).toBeVisible();
  await expect(help.getByRole("button", { name: "Close" })).toBeFocused();
  await expectAccessible(page, "keyboard shortcuts", { include: ".shortcuts-panel" });
  await page.keyboard.press("Escape");
  await expect(help).toHaveCount(0);
  await expect(heading).toBeFocused();
});

test("define: edit an objective from the UI", async () => {
  await page.goto(at("define"));
  const edit = page.getByRole("button", { name: /^Edit OBJ-\d+$/ }).first();
  await expect(edit).toBeVisible();
  const code = ((await edit.getAttribute("aria-label")) ?? "").replace("Edit ", "");
  await edit.click();
  await expect(page.getByLabel("Statement", { exact: true })).toBeFocused();
  await page.getByLabel("Owner", { exact: true }).fill("Growth team");
  await expectAccessible(page, "define (objective edit)");
  await page.getByRole("button", { name: `Save ${code}` }).click();
  await expect(page.getByText("Owner: Growth team")).toBeVisible();
  await expect(page.getByTestId("live-announcer")).toContainText(`${code} saved`);
});

test("settings: executor shows requested and resolved, one demo banner", async () => {
  await page.goto("/#/settings");
  await expect(page.locator("#executor-kind")).toHaveValue("auto");
  await expect(page.locator(".executor-resolution")).toContainText("Requested: auto · resolves to: internal");
  await expect(page.getByText(/pinned default image is pulled/)).toHaveCount(0);
  await expect(page.getByText(/Demo mode/)).toHaveCount(1);
  await expectAccessible(page, "settings");
});
