import { approvedBrief, expect, helperFor, notice, owner, signIn, test, type Task } from "./subspace";

const prURL = "https://github.com/SigmaUno/e2e/pull/42";

async function submittedTask() {
  const seeded = await approvedBrief();
  const task = seeded.plan.tasks.find((item) => item.title.endsWith(" API"))!;
  const leader = helperFor(seeded.leader);
  const worker = helperFor(seeded.worker);
  await leader.call("assign", { task_id: task.id, agent_id: seeded.worker.id });
  await worker.call("claim_task", { task_id: task.id });
  await worker.call("submit", { task_id: task.id, pr_url: prURL, summary: "done" });
  return { ...seeded, task };
}

test("task detail shows the PR panel and owner actions move the task", async ({ page }) => {
  const { task, brief, worker } = await submittedTask();
  await signIn(page);
  await page.goto(`/tasks/${task.id}`);

  await expect(page.getByRole("heading", { name: task.title, level: 1 })).toBeVisible();
  await expect(page.getByText(`${task.id} ·`).first()).toContainText(brief.id);
  await expect(page.locator(".pill[data-status]").first()).toHaveText("Submitted");
  const overview = page.locator("section").filter({ has: page.getByRole("heading", { name: "Overview", exact: true }) });
  await expect(overview.getByRole("link", { name: "Open pull request" })).toHaveAttribute("href", prURL);
  await expect(overview.getByText("Not reported")).toBeVisible();
  await expect(overview.getByText(worker.id, { exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: `Live output of ${worker.id}` }).getByText("No output yet.")).toBeVisible();

  await page.getByLabel("Max minutes").fill("90");
  await page.getByLabel("Max cost (EUR)").fill("7.5");
  await page.getByRole("button", { name: "Save budget" }).click();
  await expect(notice(page, "Budget saved.")).toBeVisible();
  await expect(page.getByText("0 / 90 min")).toBeVisible();
  await expect.poll(async () => (await owner<Task & { budget: { max_minutes: number; max_cost_eur: number } }>(`tasks/${task.id}`)).budget)
    .toEqual({ max_minutes: 90, max_cost_eur: 7.5 });

  await page.getByLabel("Reason", { exact: true }).fill("superseded by the UI task");
  await page.getByRole("button", { name: "Cancel task" }).click();
  const confirm = page.getByRole("alertdialog", { name: "Cancel task?" });
  await expect(confirm).toContainText(`${task.id} is cancelled and its lease is released.`);
  await confirm.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect.poll(async () => (await owner<Task>(`tasks/${task.id}`)).status).toBe("submitted");

  await page.getByRole("button", { name: "Cancel task" }).click();
  await page.getByRole("alertdialog", { name: "Cancel task?" }).getByRole("button", { name: "Cancel task" }).click();
  await expect(notice(page, `Cancelled ${task.id}.`)).toBeVisible();
  await expect(page.locator(".pill[data-status]").first()).toHaveText("Cancelled");
  await expect(page.getByRole("button", { name: "Cancel task" })).toHaveCount(0);

  await page.locator("form").filter({ has: page.getByRole("heading", { name: "Reopen", level: 3 }) }).getByRole("combobox").selectOption("ready");
  await page.getByRole("button", { name: "Reopen", exact: true }).click();
  await expect(notice(page, "Reopened.")).toBeVisible();
  await expect(page.locator(".pill[data-status]").first()).toHaveText("Ready");
  await expect.poll(async () => (await owner<Task>(`tasks/${task.id}`)).status).toBe("ready");
  await expect(page.getByRole("heading", { name: "Timeline" })).toBeVisible();
});

test("failing quality gates are listed and the owner overrides them with a reason", async ({ page }) => {
  const { task, brief } = await submittedTask();
  await owner(`briefs/${brief.id}`, "PATCH", {
    quality_gates: { tests: true, min_tests_passed: 1, checks: false, scope: false, questions: false, review: false, budget: false, max_pr_age_hours: 0 },
  });
  await signIn(page);
  await page.goto(`/tasks/${task.id}`);

  const panel = page.locator("section").filter({ has: page.getByRole("heading", { name: "Quality gates", exact: true }) });
  const gates = panel.getByRole("list", { name: "Quality gates" });
  await expect(gates.getByRole("listitem")).toHaveCount(1);
  await expect(gates.getByRole("listitem")).toContainText("Fail");
  await expect(gates.getByRole("listitem")).toContainText("Tests");

  await panel.getByLabel("Reason").fill("tests run outside CI");
  await panel.getByRole("button", { name: "Override 1 gate" }).click();
  await expect(gates.getByRole("listitem")).toContainText("Overridden");
  await expect(panel.getByText("Override by owner: tests run outside CI")).toBeVisible();
  await expect(panel.getByRole("button", { name: /^Override/ })).toHaveCount(0);

  await expect.poll(async () => {
    const report = await owner<{ passed: boolean; override: { reason: string } | null }>(`tasks/${task.id}/gates`);
    return [report.passed, report.override?.reason];
  }).toEqual([true, "tests run outside CI"]);
});
