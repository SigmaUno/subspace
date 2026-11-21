import { approvedBrief, expect, owner, signIn, test, type Task } from "./subspace";

test("saved views store filters, survive reloads and can be deleted", async ({ page }) => {
  const { brief, plan } = await approvedBrief();
  const [api, ui] = ["API", "UI"].map((suffix) => plan.tasks.find((task) => task.title.endsWith(` ${suffix}`))!);
  await signIn(page);
  await page.goto(`/briefs/${brief.id}`);
  const column = page.getByRole("region", { name: "Ready", exact: true });
  const shown = page.getByRole("status").filter({ hasText: / tasks$/ });
  await expect(column.getByRole("button", { name: api.title })).toBeVisible();
  await expect(column.getByRole("button", { name: ui.title })).toBeVisible();

  await page.getByRole("searchbox", { name: "Filter tasks" }).fill("API");
  await expect(column.getByRole("button", { name: ui.title })).toHaveCount(0);
  await expect(column.getByRole("button", { name: api.title })).toBeVisible();
  await expect(page).toHaveURL(/[?&]q=API/);

  await page.getByLabel("View name").fill("api only");
  await page.getByRole("button", { name: "Save view" }).click();
  await expect(page.getByRole("combobox", { name: "Saved views" })).toHaveValue("api only");

  await page.getByRole("button", { name: "Clear filters" }).click();
  await expect(column.getByRole("button", { name: ui.title })).toBeVisible();
  await expect(page.getByRole("searchbox", { name: "Filter tasks" })).toHaveValue("");

  await page.reload();
  await expect(column.getByRole("button", { name: ui.title })).toBeVisible();
  await page.getByRole("combobox", { name: "Saved views" }).selectOption("api only");
  await expect(page.getByRole("searchbox", { name: "Filter tasks" })).toHaveValue("API");
  await expect(column.getByRole("button", { name: ui.title })).toHaveCount(0);
  await expect(shown).toHaveText("1 of 2 tasks");

  await page.getByRole("button", { name: "Delete view" }).click();
  await expect(page.getByRole("combobox", { name: "Saved views" }).getByRole("option", { name: "api only" })).toHaveCount(0);
});

test("filter shortcuts and compact cards apply to the board", async ({ page }) => {
  const { brief } = await approvedBrief();
  await signIn(page);
  await page.goto(`/briefs/${brief.id}`);
  const shown = page.getByRole("status").filter({ hasText: / tasks$/ });
  await expect(shown).toHaveText("2 of 2 tasks");

  await page.getByRole("button", { name: "Compact cards" }).click();
  await expect(page.getByRole("region", { name: "Kanban board" })).toHaveAttribute("data-density", "compact");
  await page.getByRole("button", { name: "Compact cards" }).click();
  await expect(page.getByRole("region", { name: "Kanban board" })).toHaveAttribute("data-density", "comfortable");

  await page.keyboard.press("Shift+B");
  await expect(page.getByRole("checkbox", { name: "Blocked or needs owner" })).toBeChecked();
  await expect(shown).toHaveText("0 of 2 tasks");
  await expect(page).toHaveURL(/[?&]blocked=1/);
  await page.keyboard.press("Shift+X");
  await expect(shown).toHaveText("2 of 2 tasks");
  await expect(page.getByRole("button", { name: "Clear filters" })).toBeDisabled();
});

test("bulk actions assign and cancel several tasks at once", async ({ page }) => {
  const { brief, plan, worker } = await approvedBrief();
  await signIn(page);
  await page.goto(`/briefs/${brief.id}`);
  const card = (column: string, task: { title: string }) =>
    page.getByRole("region", { name: column, exact: true }).getByRole("button", { name: task.title });
  const bar = page.getByRole("region", { name: "Bulk actions" });
  for (const task of plan.tasks) await expect(card("Ready", task)).toBeVisible();

  await page.getByRole("checkbox", { name: "Select all tasks" }).check();
  await expect(bar).toContainText("2 selected");
  await bar.getByRole("button", { name: "Assign", exact: true }).click();
  await bar.getByLabel("Agent").selectOption(worker.id);
  await bar.getByRole("button", { name: "Assign 2 tasks" }).click();
  const result = page.getByRole("status").filter({ hasText: /^Assign: 2 succeeded/ });
  await expect(result).toBeVisible();
  for (const task of plan.tasks) await expect(card("Assigned", task)).toBeVisible();
  await expect.poll(async () => {
    const { tasks } = await owner<{ tasks: Task[] }>(`tasks?brief_id=${brief.id}`);
    return tasks.filter((task) => task.assignee === worker.id).length;
  }).toBe(2);
  await result.getByRole("button", { name: "Dismiss" }).click();

  for (const task of plan.tasks) await page.getByRole("checkbox", { name: `Select ${task.title}` }).check();
  await bar.getByRole("button", { name: "Cancel", exact: true }).click();
  await bar.getByLabel("Reason").fill("superseded");
  await bar.getByRole("button", { name: "Cancel 2 tasks" }).click();
  await expect(bar.getByRole("alert")).toHaveText("Cancel 2 tasks? This cannot be undone.");
  await expect.poll(async () => (await owner<{ tasks: Task[] }>(`tasks?brief_id=${brief.id}&status=cancelled`)).tasks?.length ?? 0).toBe(0);
  await bar.getByRole("button", { name: "Confirm cancel" }).click();
  await expect(page.getByRole("status").filter({ hasText: /^Cancel: 2 succeeded/ })).toBeVisible();
  for (const task of plan.tasks) await expect(card("Cancelled", task)).toBeVisible();
  await expect(bar).toHaveCount(0);
  await expect.poll(async () => (await owner<{ tasks: Task[] }>(`tasks?brief_id=${brief.id}&status=cancelled`)).tasks.length).toBe(2);
});
