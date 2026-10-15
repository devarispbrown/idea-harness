import AxeBuilder from "@axe-core/playwright";
import { expect, type Page } from "@playwright/test";

// Accepted exceptions: axe rule ids that may report a serious/critical
// violation on a given page, each with the reason it is accepted rather than
// fixed. Empty means every serious/critical violation fails the suite; add a
// row only with a reason a reviewer can check.
export const A11Y_EXCEPTIONS: { rule: string; page: RegExp; reason: string }[] = [];

// The colour schemes every page is checked in. The stylesheet re-themes the
// whole app under prefers-color-scheme: dark, and a token that passes
// contrast on the light canvas can fail on the dark one (and the reverse), so
// checking light alone left half of what people see unchecked.
const SCHEMES = ["light", "dark"] as const;

// expectAccessible runs axe (WCAG 2 A/AA) against what the page shows now, in
// the light and the dark colour scheme, and fails on any serious or critical
// violation not listed above. Moderate and minor findings do not fail: the
// gate is the two levels axe itself calls blocking. The page is left in the
// light scheme.
//
// A modal dialog is checked with the whole page: while one is open the app
// root is inert (focus.ts, inertOutside), so axe sees only the dialog and
// what stays reachable, exactly what a screen reader gets.
export async function expectAccessible(page: Page, label: string) {
  // Let the 2.5s poll settle so axe sees the page, not its loading state.
  await page.waitForLoadState("networkidle").catch(() => {});
  const failures: string[] = [];
  try {
    for (const scheme of SCHEMES) {
      await page.emulateMedia({ colorScheme: scheme });
      const found = await violations(page, `${label} (${scheme})`);
      if (found) failures.push(found);
    }
  } finally {
    await page.emulateMedia({ colorScheme: "light" });
  }
  // IDEA_E2E_A11Y=report logs instead of failing, to see every page's
  // violations in one run while fixing them; CI never sets it.
  if (process.env.IDEA_E2E_A11Y === "report") {
    for (const f of failures) console.log(f);
    return;
  }
  expect(failures, `axe found serious/critical violations:\n${failures.join("\n")}`).toEqual([]);
}

// violations runs axe once and describes its blocking findings, or returns ""
// when there are none.
async function violations(page: Page, label: string): Promise<string> {
  // IDEA_E2E_SHOTS=<dir> keeps a full-page screenshot of every page checked,
  // for looking at what the suite saw.
  if (process.env.IDEA_E2E_SHOTS) {
    await page.screenshot({
      path: `${process.env.IDEA_E2E_SHOTS}/${label.replace(/[^a-z0-9]+/gi, "-")}.png`,
      fullPage: true,
    });
  }
  const results = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  const url = page.url();
  const blocking = results.violations.filter(
    (v) =>
      (v.impact === "serious" || v.impact === "critical") &&
      !A11Y_EXCEPTIONS.some((e) => e.rule === v.id && e.page.test(url)),
  );
  if (!blocking.length) return "";
  return (
    `[a11y] ${label} (${url}):\n` +
    blocking
      .map(
        (v) =>
          `${v.impact} ${v.id}: ${v.help}\n` +
          v.nodes
            .slice(0, 5)
            .map((n) => `    ${n.target.join(" ")} — ${n.failureSummary?.split("\n").join(" ")}`)
            .join("\n"),
      )
      .join("\n")
  );
}
