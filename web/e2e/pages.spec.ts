import { approvedBrief, expect, helperFor, owner, signIn, test } from "./subspace";

test.describe("mission control", () => {
  test("renders every tile with live data and hides controls for viewers", async ({ page }) => {
    const { leader, plan, worker } = await approvedBrief();
    const task = plan.tasks.find((item) => item.title.endsWith(" API"))!;
    await helperFor(leader).call("assign", { task_id: task.id, agent_id: worker.id });
    await helperFor(worker).call("claim_task", { task_id: task.id });

    await signIn(page);
    await page.goto("/mission");
    await expect(page).toHaveTitle("Mission control · Subspace");
    await expect(page.getByRole("heading", { name: "Mission control", level: 1 })).toBeVisible();
    await expect(page.getByRole("status").filter({ hasText: "Live" })).toBeVisible();
    for (const name of ["Agents", "Tasks by status", "PRs in flight", "Merge train", "Spend today", "Alerts", "Briefs", "Stalled tasks", "Provider breakers"]) {
      await expect(page.getByRole("heading", { name, level: 2 })).toBeVisible();
    }
    await expect(page.getByRole("img", { name: /^\d+ tasks$/ })).toBeVisible();
    await expect(page.getByRole("img", { name: /: \d+% complete$/ }).first()).toBeVisible();
    await expect(page.getByRole("region", { name: "Recent activity" })).not.toContainText("Waiting for activity");
    await expect(page.getByRole("button", { name: "Fullscreen" })).toBeVisible();
    await expect(page.getByRole("main").getByRole("alert")).toHaveCount(0);

    const counts = (await owner<{ counts: { tasks: Record<string, number> } }>("state")).counts.tasks;
    const total = Object.values(counts).reduce((sum, count) => sum + count, 0);
    await expect(page.getByRole("img", { name: `${total} tasks` })).toBeVisible();

    await page.goto("/mission?viewer=1");
    await expect(page.getByRole("heading", { name: "Mission control", level: 1 })).toBeVisible();
    await expect(page.getByRole("button", { name: "Fullscreen" })).toHaveCount(0);
  });
});

test.describe("replay", () => {
  test("scrubbing the timeline moves the board between past and live states", async ({ page }) => {
    const { brief, leader, plan, worker } = await approvedBrief();
    const task = plan.tasks.find((item) => item.title.endsWith(" API"))!;
    await helperFor(leader).call("assign", { task_id: task.id, agent_id: worker.id });
    await helperFor(worker).call("claim_task", { task_id: task.id });

    await signIn(page);
    await page.goto(`/briefs/${brief.id}/replay`);
    await expect(page.getByRole("heading", { name: "Replay", level: 1 })).toBeVisible();
    const board = page.getByRole("region", { name: "Board at the replay position" });
    const card = (column: string) => board.getByRole("region", { name: column, exact: true }).getByRole("article", { name: new RegExp(`^${task.id} `) });
    const step = page.getByText(/^Step \d+ of \d+/);
    const slider = page.getByRole("slider", { name: "Replay position" });

    await expect(card("In progress")).toBeVisible();
    await expect(page.getByRole("button", { name: "Live" })).toBeDisabled();
    const total = Number((await step.textContent())!.match(/of (\d+)/)![1]);
    expect(total).toBeGreaterThan(3);
    await expect(slider).toHaveAttribute("max", String(total - 1));

    await slider.focus();
    await page.keyboard.press("Home");
    await expect(step).toContainText(`Step 1 of ${total}`);
    await expect(card("In progress")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Live" })).toBeEnabled();

    await page.getByRole("button", { name: "Next step" }).click();
    await expect(step).toContainText(`Step 2 of ${total}`);
    await expect(page.getByRole("button", { name: "Previous step" })).toBeEnabled();

    await page.getByRole("button", { name: "Live" }).click();
    await expect(step).toContainText(`Step ${total} of ${total}`);
    await expect(card("In progress")).toBeVisible();

    await page.getByRole("button", { name: /approve plan · owner/ }).click();
    await expect(card("Ready")).toBeVisible();
    await expect(card("In progress")).toHaveCount(0);

    const proposed = page.getByRole("button", { name: new RegExp(`propose plan · ${leader.id}`) });
    await proposed.click();
    await expect(proposed).toHaveAttribute("aria-current", "step");
    await expect(card("Backlog")).toBeVisible();
    await expect(card("Ready")).toHaveCount(0);
  });
});

test.describe("onboarding", () => {
  test("checklist progress advances when the charter is saved", async ({ page }) => {
    await approvedBrief();
    await owner("charter", "PUT", { text: "" });
    await page.addInitScript(() => localStorage.setItem("subspace.onboarding", "active"));
    await signIn(page);
    await page.goto("/");

    const checklist = page.getByRole("region", { name: "Getting started" });
    await expect(checklist.getByRole("heading", { name: "Getting started" })).toBeVisible();
    const steps = checklist.getByRole("listitem").filter({ has: page.locator(".pill[data-status]") });
    await expect(steps.first()).toContainText("1. Set the charter");
    await expect(steps.first().locator(".pill")).toHaveText("Next");
    const done = async () => await checklist.isVisible()
      ? await checklist.locator(".pill", { hasText: /^Done$/ }).count()
      : 4;
    const before = await done();
    expect(before).toBeLessThan(4);

    await checklist.getByLabel("Charter", { exact: true }).fill("Agents ask the owner before touching main.");
    await checklist.getByRole("button", { name: "Save charter" }).click();
    await expect.poll(done).toBe(before + 1);
    await expect.poll(async () => (await owner<{ text: string }>("charter")).text).toBe("Agents ask the owner before touching main.");

    if (await checklist.isVisible()) {
      await checklist.getByRole("button", { name: "Dismiss checklist" }).click();
      await expect(checklist).toHaveCount(0);
      await page.reload();
      await expect(page.getByRole("heading", { name: "Swarm overview" })).toBeVisible();
      await expect(checklist).toHaveCount(0);
    }
  });
});

test.describe("mobile viewport", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("tab bar and more sheet replace the top navigation", async ({ page }) => {
    await signIn(page);
    await page.goto("/");
    const tabbar = page.getByRole("navigation", { name: "Primary" });
    await expect(tabbar).toBeVisible();
    await expect(page.getByRole("navigation", { name: "Workspace" })).toBeHidden();
    await expect(tabbar.getByRole("button", { name: /^Inbox/ })).toHaveAttribute("aria-pressed", "true");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

    await tabbar.getByRole("link", { name: "Agents" }).click();
    await expect(page).toHaveURL(/\/agents$/);
    await expect(page.getByRole("heading", { name: "Agents", level: 1 })).toBeVisible();
    await expect(tabbar.getByRole("link", { name: "Agents" })).toHaveAttribute("aria-current", "page");

    await tabbar.getByRole("button", { name: "More" }).click();
    const sheet = page.getByRole("dialog", { name: "More" });
    for (const name of ["Costs", "Plans", "Mission control", "Settings", "Charter"]) {
      await expect(sheet.getByRole("link", { name, exact: true })).toBeVisible();
    }
    await sheet.getByRole("link", { name: "Settings", exact: true }).click();
    await expect(page).toHaveURL(/\/settings$/);
    await expect(sheet).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Settings", level: 1 })).toBeVisible();
    await expect(tabbar.getByRole("button", { name: "More" })).toHaveAttribute("aria-current", "page");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

    await page.goto("/");
    const inbox = page.getByRole("complementary", { name: "Decision inbox" });
    await expect(inbox).toBeVisible();
    await tabbar.getByRole("button", { name: /^Inbox/ }).click();
    await expect(inbox).toHaveCount(0);
    await tabbar.getByRole("button", { name: /^Inbox/ }).click();
    await expect(inbox).toBeVisible();
    await expect(tabbar.getByRole("button", { name: /^Inbox/ })).toHaveAttribute("aria-pressed", "true");
  });
});
