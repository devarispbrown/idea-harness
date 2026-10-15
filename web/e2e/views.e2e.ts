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

// api reads the daemon as the page does: same origin, with the page's token.
async function api<T>(path: string): Promise<T> {
  return page.evaluate(async (p) => {
    const token = document.querySelector('meta[name="idea-token"]')?.getAttribute("content") ?? "";
    const res = await fetch(`/api/v1${p}`, { headers: token ? { "X-Idea-Token": token } : {} });
    if (!res.ok) throw new Error(`${p}: ${res.status}`);
    return res.json();
  }, path) as Promise<T>;
}

test.beforeAll(async ({ browser }, testInfo) => {
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
  const prd = artifacts.filter((a) => a.kind === artifacts[0]?.kind);
  routes = [
    { label: "home", hash: `${p}/` },
    { label: "settings", hash: `${p}/settings` },
    { label: "automations", hash: `${p}/automations` },
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
  if (prd.length >= 2) routes.push({ label: "artifact diff", hash: `${i}/diff/${prd[0].id}/${prd[1].id}` });
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
