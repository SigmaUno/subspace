import AxeBuilder from "@axe-core/playwright";
import type { Page } from "@playwright/test";
import { themes } from "../lib/theme";
import { expect, owner, signIn, test } from "../e2e/subspace";

const fixed = ["/", "/briefs", "/agents", "/bots", "/plans", "/questions", "/capacity", "/charter", "/usage", "/trends", "/instructions", "/files", "/audit", "/prompts", "/hillclimb", "/mergequeue"];

async function screens() {
  const [{ briefs }, { agents }, { tasks }] = await Promise.all([
    owner<{ briefs: { id: string }[] }>("briefs"),
    owner<{ agents: { id: string }[] }>("agents"),
    owner<{ tasks: { id: string }[] }>("tasks"),
  ]);
  expect(briefs.length).toBeGreaterThan(0);
  expect(agents.length).toBeGreaterThan(0);
  expect(tasks.length).toBeGreaterThan(0);
  const [brief] = briefs;
  return [
    ...fixed,
    `/briefs/${brief.id}`,
    `/briefs/${brief.id}/graph`,
    `/briefs/${brief.id}/report`,
    `/agents/${agents[0].id}`,
    `/tasks/${tasks[0].id}`,
  ];
}

async function scan(page: Page) {
  const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "best-practice"]).analyze();
  return violations
    .filter((violation) => violation.impact === "serious" || violation.impact === "critical")
    .map((violation) => ({ rule: violation.id, impact: violation.impact, help: violation.help, targets: violation.nodes.map((node) => node.target.join(" ")) }));
}

async function settle(page: Page) {
  await expect(page.locator("main").first()).toBeVisible();
  await expect(page.locator("[role=status][aria-label=Loading]")).toHaveCount(0);
}

for (const theme of Object.keys(themes)) {
  test.describe(theme, () => {
    test.use({ colorScheme: theme === "dark" ? "dark" : "light" });

    test("login screen has no serious accessibility violations", async ({ page }) => {
      await page.goto("/login");
      await page.evaluate((name) => { document.documentElement.dataset.theme = name; }, theme);
      expect(await scan(page)).toEqual([]);
    });

    test("every owner screen has no serious accessibility violations", async ({ page }) => {
      await signIn(page);
      const failures: Record<string, unknown> = {};
      for (const path of await screens()) {
        await page.goto(path);
        await page.evaluate((name) => { document.documentElement.dataset.theme = name; }, theme);
        await settle(page);
        const found = await scan(page);
        if (found.length) failures[path] = found;
      }
      expect(failures).toEqual({});
    });

    test("decision inbox and thread panel have no serious accessibility violations", async ({ page }) => {
      await signIn(page);
      await page.goto("/agents");
      await page.evaluate((name) => { document.documentElement.dataset.theme = name; }, theme);
      await settle(page);
      await page.getByRole("button", { name: /^Decisions|^Inbox/ }).filter({ visible: true }).first().click();
      expect(await scan(page)).toEqual([]);
    });
  });
}
