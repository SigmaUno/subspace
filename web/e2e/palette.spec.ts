import type { Page } from "@playwright/test";
import { expect, seedBrief, signIn, test } from "./subspace";

const palette = (page: Page) => page.getByRole("dialog", { name: "Search and commands" });

test("command palette navigates with the keyboard and closes on escape", async ({ page }) => {
  await signIn(page);
  await page.goto("/agents");
  await expect(page.getByRole("heading", { name: "Agents", level: 1 })).toBeVisible();

  await page.keyboard.press("Control+k");
  const input = palette(page).getByRole("combobox", { name: "Search Subspace or run a command" });
  await expect(input).toBeFocused();
  const options = palette(page).getByRole("listbox", { name: "Results" }).getByRole("option");
  await input.fill("go to ");
  await expect(options.first()).toHaveAttribute("aria-selected", "true");
  const count = await options.count();
  expect(count).toBeGreaterThan(3);
  await page.keyboard.press("ArrowDown");
  await expect(options.nth(1)).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("ArrowUp");
  await page.keyboard.press("ArrowUp");
  await expect(options.nth(count - 1)).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Escape");
  await expect(palette(page)).toHaveCount(0);

  await page.keyboard.press("Control+k");
  await input.fill("go to settings");
  await expect(options).toHaveCount(1);
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/settings$/);
  await expect(page.getByRole("heading", { name: "Settings", level: 1 })).toBeVisible();
  await expect(palette(page)).toHaveCount(0);
});

test.fixme("command palette search finds briefs and tasks (proxy rejects the search redirect)", async ({ page }) => {
  const { brief, plan } = await seedBrief();
  const task = plan.tasks.find((item) => item.title.endsWith(" API"))!;
  await signIn(page);
  await page.goto("/agents");

  await page.keyboard.press("Control+k");
  const input = palette(page).getByRole("combobox");
  await input.fill(brief.title.toLowerCase());
  await expect(palette(page).getByRole("option", { name: new RegExp(`^${brief.id}\\b`) })).toBeVisible();
  await input.fill(task.title.toLowerCase());
  await expect(palette(page).getByRole("option", { name: new RegExp(`^${task.id}\\b`) })).toBeVisible();
  await palette(page).getByRole("option", { name: new RegExp(`^${task.id}\\b`) }).click();
  await expect(page).toHaveURL(new RegExp(`/tasks/${task.id}$`));

  await page.keyboard.press("Control+k");
  await input.fill("zzzz-no-such-thing-zzzz");
  await expect(palette(page).getByText(/^No matches for/)).toBeVisible();
});
