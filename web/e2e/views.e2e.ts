import { expect, test, type Page } from "@playwright/test";
import { expectAccessible } from "./a11y";

// Every route, at a phone width (390px) and a desktop width (1280px), in the
// light and the dark colour scheme: exactly one h1, no horizontal overflow
// (the page never scrolls sideways; wide content scrolls inside its own
// box), and axe. Runs last (files run in name order), against the state the
// journey and the settings suite left: an initiative built, applied and
// verified, so the views have real content to lay out.
// Not serial: a width that fails must not hide what the other one finds.
test.describe.configure({ mode: "default" });

const VIEWPORTS = [
  { name: "phone", width: 390, height: 844 },
  { name: "desktop", width: 1280, height: 900 },
] as const;

let page: Page;
let routes: { label: string; hash: string }[] = [];

// api calls the daemon as the page does: same origin, with the page's token.
async function api<T>(path: string, method = "GET", body?: unknown): Promise<T> {
  return page.evaluate(
    async ({ p, m, b }) => {
      const token = document.querySelector('meta[name="idea-token"]')?.getAttribute("content") ?? "";
      const headers: Record<string, string> = token ? { "X-Idea-Token": token } : {};
      if (b !== undefined) headers["Content-Type"] = "application/json";
      const res = await fetch(`/api/v1${p}`, { method: m, headers, body: b === undefined ? undefined : JSON.stringify(b) });
      if (!res.ok) throw new Error(`${m} ${p}: ${res.status}`);
      return res.json();
    },
    { p: path, m: method, b: body },
  ) as Promise<T>;
}

// seedPRDRevision gives the artifact-diff route a diff with real added and
// removed lines. The demo provider writes the same PRD every time, so a plain
// revise diffs as "no change"; a person's edit to OBJ-1 is kept by the
// revision, which then files its own objective under the next code, and the
// PRD's OBJ lines change. A second initiative, so the journey's one is left
// as the earlier suites put it. Returns the PRD's first and newest version.
async function seedPRDRevision(pid: string): Promise<{ iid: string; from: string; to: string }> {
  const created = await api<{ initiative_id: string }>(`/projects/${pid}/initiatives`, "POST", {
    title: "A meal planner for busy weeks",
    description: "A meal planner for busy weeks",
  });
  const iid = created.initiative_id;
  const base = `/projects/${pid}/initiatives/${iid}`;
  const settled = async () => {
    const list = await api<{ id: string; status: string }[]>(`/projects/${pid}/initiatives`);
    return list.find((x) => x.id === iid)?.status !== "running";
  };
  const prds = async () =>
    (await api<{ id: string; kind: string; version: number }[]>(`${base}/artifacts`)).filter((a) => a.kind === "prd");
  await expect.poll(async () => (await settled()) && (await prds()).length, { timeout: 60_000 }).toBe(1);
  const objectives = await api<{ id: string }[]>(`${base}/objectives`);
  await api(`${base}/objectives/${objectives[0].id}`, "PATCH", { statement: "Busy households plan a week of meals in five minutes" });
  await api(`${base}/revise`, "POST", {});
  await expect.poll(async () => (await settled()) && (await prds()).length, { timeout: 60_000 }).toBe(2);
  const versions = (await prds()).sort((a, b) => a.version - b.version);
  return { iid, from: versions[0].id, to: versions[versions.length - 1].id };
}

test.beforeAll(async ({ browser }, testInfo) => {
  // Seeding the diff runs the demo M1 pipeline and a revision.
  test.setTimeout(3 * 60_000);
  const context = await browser.newContext({ baseURL: testInfo.project.use.baseURL });
  page = await context.newPage();
  await page.goto("/");
  const projects = await api<{ project_id: string }[]>("/projects");
  let pid = "";
  let iid = "";
  for (const p of projects) {
    const list = await api<{ id: string; title: string; description: string }[]>(`/projects/${p.project_id}/initiatives`);
    const hit = list.find((i) => /recipe/i.test(`${i.title} ${i.description}`));
    if (hit) {
      pid = p.project_id;
      iid = hit.id;
      break;
    }
  }
  expect(iid, "the seeded initiative").not.toBe("");
  const artifacts = await api<{ id: string; kind: string; version: number }[]>(`/projects/${pid}/initiatives/${iid}/artifacts`);
  const decisions = await api<{ id: string }[]>(`/projects/${pid}/initiatives/${iid}/decisions`).catch(() => []);
  const p = `#/p/${pid}`;
  const i = `${p}/i/${iid}`;
  const diff = await seedPRDRevision(pid);
  routes = [
    { label: "home", hash: `${p}/` },
    { label: "settings", hash: `${p}/settings` },
    { label: "automations", hash: `${p}/automations` },
    { label: "remote runs", hash: `${p}/runs` },
    { label: "queue", hash: `${p}/queue` },
    { label: "not found", hash: `${p}/no-such-page` },
    ...(["explore", "define", "design", "plan", "build", "verify"] as const).map((ph) => ({ label: ph, hash: `${i}/${ph}` })),
    { label: "health", hash: `${i}/health` },
    { label: "activity", hash: `${i}/changed` },
    { label: "gaps", hash: `${i}/gaps` },
    { label: "outcomes", hash: `${i}/outcomes` },
    { label: "quality", hash: `${i}/quality` },
    { label: "lineage", hash: `${i}/lineage/task/TASK-1` },
    { label: "decision", hash: `${i}/decision/${decisions[0]?.id ?? "not-a-decision"}` },
    { label: "unknown initiative", hash: `${p}/i/0123456789abcdef0123456789abcdef/gaps` },
  ];
  if (artifacts[0]) routes.push({ label: "document", hash: `${i}/doc/${artifacts[0].id}/${artifacts[0].version}` });
  routes.push({ label: "artifact diff", hash: `${p}/i/${diff.iid}/diff/${diff.from}/${diff.to}` });
});
test.afterAll(async () => {
  await page.context().close();
});

// overflowing describes what sticks out past the viewport's right edge, or
// "" when the page fits. scrollWidth alone cannot see it: html and body clip
// horizontal overflow (styles.css), so a too-wide element does not scroll
// the page -- it is cut off, which is worse. So every visible element is
// checked too, unless a box of its own that fits the viewport scrolls or
// clips it (the diff pane, the phone phase rail, an ellipsised title).
async function overflowing(): Promise<string> {
  return page.evaluate(() => {
    const W = window.innerWidth + 0.5;
    const doc = document.scrollingElement ?? document.documentElement;
    const out: string[] = [];
    if (doc.scrollWidth > window.innerWidth) out.push(`scrollWidth ${doc.scrollWidth} > innerWidth ${window.innerWidth}`);
    const contained = (el: HTMLElement) => {
      for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
        if (getComputedStyle(a).overflowX !== "visible" && a.getBoundingClientRect().right <= W) return true;
      }
      return false;
    };
    // Collapsed disclosures too: what is in them is laid out the moment
    // someone opens one. Put back as they were afterwards.
    const closed = Array.from(document.querySelectorAll("details:not([open])"));
    for (const d of closed) d.setAttribute("open", "");
    const offenders: HTMLElement[] = [];
    for (const el of Array.from(document.querySelectorAll<HTMLElement>("body *"))) {
      const r = el.getBoundingClientRect();
      if (r.width <= 1 || r.height <= 1) continue;
      const cs = getComputedStyle(el);
      if (cs.visibility === "hidden" || cs.position === "fixed") continue;
      // A box that fits, with text spilling out of it (an unbreakable path):
      // the text, not an element, is what crosses the edge.
      const spills = cs.overflowX === "visible" && el.scrollWidth > el.clientWidth + 1 && r.left + el.scrollWidth > W;
      if (r.right <= W && !spills) continue;
      if (contained(el)) continue;
      offenders.push(el);
    }
    // The innermost offenders name the culprit: a spill shows in every
    // ancestor's scrollWidth too.
    const culprits = offenders.filter((o) => !offenders.some((x) => x !== o && o.contains(x)));
    for (const el of culprits.slice(0, 5)) {
      const cls = typeof el.className === "string" && el.className ? "." + el.className.trim().split(/\s+/).join(".") : "";
      const r = el.getBoundingClientRect();
      out.push(`${el.tagName.toLowerCase()}${cls} right=${Math.round(Math.max(r.right, r.left + el.scrollWidth))} "${(el.textContent ?? "").trim().slice(0, 60)}"`);
    }
    for (const d of closed) d.removeAttribute("open");
    return out.join("; ");
  });
}

for (const vp of VIEWPORTS) {
  test(`every route at ${vp.width}px: one h1, no sideways scroll, axe in both schemes`, async () => {
    test.setTimeout(20 * 60_000);
    await page.setViewportSize({ width: vp.width, height: vp.height });
    const problems: string[] = [];
    for (const r of routes) {
      await page.goto(`/${r.hash}`);
      // The page's own heading, once it has rendered (pages render theirs
      // after their first fetch).
      await expect(page.locator("main h1").first(), `${r.label}: an h1`).toBeVisible();
      await page.waitForLoadState("networkidle").catch(() => {});
      if (r.label === "artifact diff") {
        // The diff itself, not its loading line or "(no change)".
        await expect(page.locator(".diff-line.diff-added").first(), "the diff's added lines").toBeVisible();
        await expect(page.locator(".diff-line.diff-removed").first(), "the diff's removed lines").toBeVisible();
      }
      const h1s = await page.locator("h1").allInnerTexts();
      if (h1s.length !== 1) problems.push(`${r.label} @${vp.width}: ${h1s.length} h1s (${h1s.join(" | ")})`);
      for (const scheme of ["light", "dark"] as const) {
        await page.emulateMedia({ colorScheme: scheme });
        const o = await overflowing();
        if (o) problems.push(`${r.label} @${vp.width} ${scheme}: ${o}`);
      }
      await page.emulateMedia({ colorScheme: "light" });
      await expectAccessible(page, `${r.label} @${vp.width}`);
    }
    // Not vacuous: the shell routes, all six phases and the graph views.
    expect(routes.length).toBeGreaterThanOrEqual(20);
    expect(problems, problems.join("\n")).toEqual([]);
  });
}

// The demo banner sits above the shell; a sidebar a full screen tall used to
// end that far below the window, leaving Settings off screen until the page
// scrolled, and a sidebar sized only for the top of the page left a gap under
// it once the page scrolled. The suite runs in demo mode, so the banner is up.
test("the sidebar fills the window under the demo banner, at the top and scrolled", async () => {
  await page.setViewportSize({ width: 1280, height: 640 });
  const build = routes.find((r) => r.hash.endsWith("/build"));
  expect(build, "a build route in the sweep").toBeTruthy();
  await page.goto(`/${build!.hash}`);
  await expect(page.locator(".banner-app")).toBeVisible();
  const settings = page.locator(".phase-nav").getByRole("button", { name: /Settings/ }).last();
  await expect(settings).toBeVisible();
  for (const where of ["top", "bottom"] as const) {
    const m = await page.evaluate((where) => {
      window.scrollTo(0, where === "top" ? 0 : document.documentElement.scrollHeight);
      const nav = document.querySelector(".phase-nav")!;
      nav.scrollTop = nav.scrollHeight;
      const btn = [...nav.querySelectorAll("button")].filter((b) => /Settings/.test(b.textContent ?? "")).pop()!;
      const banner = document.querySelector(".banner-app")!.getBoundingClientRect();
      const r = nav.getBoundingClientRect();
      return { settings: btn.getBoundingClientRect().bottom, navTop: r.top, navBottom: r.bottom, bannerBottom: banner.bottom };
    }, where);
    expect(m.settings, `${where}: Settings on screen`).toBeLessThanOrEqual(640);
    expect(Math.round(m.navBottom), `${where}: the sidebar reaches the bottom`).toBe(640);
    expect(Math.round(m.navTop), `${where}: the sidebar starts under the banner`).toBe(Math.round(m.bannerBottom));
  }
});
