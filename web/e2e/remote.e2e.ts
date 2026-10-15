import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { chromium, expect, test, type Browser, type Page } from "@playwright/test";
import { expectAccessible } from "./a11y";
import { e2eDir, e2ePort, e2eWorkDir } from "./env";

// Remote use (UI plan Phase 3), against the same hermetic demo daemon as the
// journey:
//
//   1. a second project, and the switcher: deep links name their project,
//      the choice is remembered;
//   2. a phone on another device: Chromium resolves remote.test to the
//      loopback daemon (demo-daemon.sh allows that host), so the page is
//      served WITHOUT the token -- exactly what a phone over `tailscale serve`
//      gets. It signs in with a pairing code from `idea ui pair`, switches to
//      the second project in the phone's top bar, answers a question, reviews
//      a change set's diff and approves and applies it at 390px -- every
//      write on a session cookie plus CSRF token -- then signs out.
test.describe.configure({ mode: "serial" });

const work = e2eWorkDir();
const idea = resolve(e2eDir, "../../bin/idea");
const env: Record<string, string> = Object.fromEntries(
  readFileSync(join(work, "env"), "utf8")
    .trim()
    .split("\n")
    .map((l) => l.split("=", 2) as [string, string]),
);
const cliEnv = { ...process.env, IDEA_HARNESS_STATE: env.IDEA_HARNESS_STATE, IDEA_HARNESS_ADDR: env.IDEA_HARNESS_ADDR };
const project2Dir = join(work, "grocery");

function cli(args: string[], cwd = project2Dir): string {
  return execFileSync(idea, args, { cwd, env: cliEnv, encoding: "utf8", timeout: 120_000 });
}

async function api<T>(path: string): Promise<T> {
  const token = readFileSync(join(env.IDEA_HARNESS_STATE, "token"), "utf8").trim();
  const res = await fetch(`http://${env.IDEA_HARNESS_ADDR}/api/v1${path}`, { headers: { "X-Idea-Token": token } });
  return (await res.json()) as T;
}

let p1 = "";
let p2 = "";
let ini2 = "";

test.beforeAll(async () => {
  mkdirSync(project2Dir, { recursive: true });
  execFileSync("git", ["init", "-q"], { cwd: project2Dir });
  cli(["init"]);
  cli(["new", "A shared grocery list for families"]);
  const projects = await api<{ project_id: string; root_path: string }[]>("/projects");
  p1 = projects.find((p) => !p.root_path.endsWith("grocery"))!.project_id;
  p2 = projects.find((p) => p.root_path.endsWith("grocery"))!.project_id;
  const inis = await api<{ id: string }[]>(`/projects/${p2}/initiatives`);
  ini2 = inis[0].id;
});

test("the project switcher: deep links name the project, the choice is remembered", async ({ browser }) => {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto("/");
  const switcher = page.getByLabel("Project");
  await expect(switcher).toBeVisible();
  await expect(switcher.locator("option")).toHaveCount(2);
  // The bare address is canonicalized to name the project shown.
  await expect(page).toHaveURL(new RegExp(`#/p/${p1}/$`));
  await expectAccessible(page, "home (two projects)");

  await switcher.selectOption(p2);
  await expect(page).toHaveURL(new RegExp(`#/p/${p2}/$`));
  await expect(page.getByText("A shared grocery list for families").first()).toBeVisible();
  await expect(page.getByText("A recipe sharing app for home cooks")).toHaveCount(0);

  // Remembered: a bare address opens the last project chosen.
  await page.goto("/#/");
  await expect(page).toHaveURL(new RegExp(`#/p/${p2}/$`));
  // A deep link into the other project switches to it.
  await page.goto(`/#/p/${p1}/`);
  await expect(page.getByText("A recipe sharing app for home cooks").first()).toBeVisible();
  await expect(switcher).toHaveValue(p1);
  // A project the daemon does not serve is said to be missing.
  await page.goto("/#/p/0123456789abcdef0123456789abcdef/");
  await page.reload();
  await expect(page.getByRole("heading", { name: "Project not found" })).toBeVisible();
  await context.close();
});

test.describe("a phone on another device", () => {
  let remote: Browser;
  let page: Page;
  const base = `http://remote.test:${e2ePort()}`;

  test.beforeAll(async () => {
    remote = await chromium.launch({ args: ["--host-resolver-rules=MAP remote.test 127.0.0.1"] });
    const context = await remote.newContext({
      viewport: { width: 390, height: 844 },
      isMobile: true,
      hasTouch: true,
      userAgent:
        "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
    });
    page = await context.newPage();
  });
  test.afterAll(async () => {
    await remote.close();
  });

  test("is served no token and signs in with a pairing code", async () => {
    await page.goto(base + "/");
    await expect(page.getByRole("heading", { name: "Sign in to idea-harness" })).toBeVisible();
    expect(await page.locator('meta[name="idea-token"]').getAttribute("content")).toBe("");
    await expectAccessible(page, "sign in (mobile)");

    // A wrong code is refused with what to do next.
    await page.getByLabel("Pairing code").fill("AAAAAAAAAA");
    await page.getByRole("button", { name: "Sign in" }).click();
    await expect(page.getByRole("alert")).toContainText("idea ui pair");

    const code = (JSON.parse(cli(["ui", "pair", "--label", "e2e phone", "--json"])) as { code: string }).code;
    await page.getByLabel("Pairing code").fill(code);
    await page.getByLabel(/Name this device/).fill("e2e phone");
    await page.getByRole("button", { name: "Sign in" }).click();
    await expect(page.getByRole("heading", { name: "Initiatives" })).toBeVisible();
    expect(cli(["ui", "sessions"], work)).toContain("e2e phone");
  });

  test("switches project in the top bar and answers a question at 390px", async () => {
    await page.getByLabel("Project").selectOption(p2);
    await expect(page.getByText("A shared grocery list for families").first()).toBeVisible();
    await page.goto(`${base}/#/p/${p2}/i/${ini2}/define`);
    const answer = page.getByLabel("Answer to: Who is the primary user?");
    await expect(answer).toBeVisible();
    await expectAccessible(page, "define (mobile, remote)");
    await answer.fill("Parents planning the weekly shop");
    await page.getByRole("button", { name: "Answer", exact: true }).click();
    await expect(page.getByText("Parents planning the weekly shop")).toBeVisible();
  });

  test("reviews the diff and approves from the sticky bar, then applies", async () => {
    // The plan and the run are driven from the CLI, as on the desk; the phone
    // is where the decision is made.
    cli(["advance", "--wait"]);
    try {
      cli(["approve-plan"]);
    } catch {
      // The demo's plan review usually approves it on its own.
    }
    cli(["execute", "--wait"]);

    // The queue lists it, from the phone's top bar.
    await page.goto(`${base}/#/p/${p2}/`);
    await page.getByRole("button", { name: /Needs you/ }).click();
    await expect(page.getByRole("heading", { name: "Needs you" })).toBeVisible();
    await expectAccessible(page, "queue (mobile)");

    await page.goto(`${base}/#/p/${p2}/i/${ini2}/build`);
    const card = page.locator(".cs-card", { hasText: "CHG-1" });
    await expect(card.locator(".cs-pill")).toHaveText(/review required/i, { timeout: 60_000 });
    await card.getByRole("button", { name: "View diff" }).click();
    await expect(card.locator(".cs-file-path").first()).toBeVisible();
    // Wrapped by default on a phone; nothing scrolls the page sideways.
    await expect(card.getByLabel("Wrap long lines")).toBeChecked();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow).toBeLessThanOrEqual(1);
    await expectAccessible(page, "build diff (mobile)");

    // The decision bar is sticky and on screen with the diff open.
    const bar = card.locator(".cs-decision-bar");
    expect(await bar.evaluate((el) => getComputedStyle(el).position)).toBe("sticky");
    const box = await bar.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.y + box!.height).toBeLessThanOrEqual(844 + 1);

    await bar.getByRole("button", { name: "Approve", exact: true }).click();
    await expect(card.locator(".cs-pill")).toHaveText(/approved/i);
    await card.getByRole("button", { name: "Apply" }).click();
    await expect(card.locator(".cs-pill")).toHaveText(/^applied$/i, { timeout: 60_000 });
  });

  test("lists its session in Settings and signs out", async () => {
    await page.goto(`${base}/#/p/${p2}/settings`);
    const panel = page.locator(".remote-access-panel");
    await expect(panel.getByText("e2e phone")).toBeVisible();
    await expect(panel.getByText("this device", { exact: true })).toBeVisible();
    // Push needs a secure context; a plain-HTTP remote address says so.
    await expect(page.locator(".notifications-panel")).toContainText("secure page");
    await expectAccessible(page, "settings (mobile, remote)");
    await panel.getByRole("button", { name: "Sign out of this device" }).click();
    await expect(page.getByRole("heading", { name: "Sign in to idea-harness" })).toBeVisible();
    expect(cli(["ui", "sessions"], work)).toMatch(/revoked/);
  });
});
