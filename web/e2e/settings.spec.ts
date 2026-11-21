import { apiURL, expect, notice, openSettings, owner, ownerToken, signIn, test, uniqueID } from "./subspace";

type Capacity = { global_max_agents: number; limits: { provider: string; model: string; max_concurrency: number; requests_per_min: number }[] | null };
type SpendLimits = { daily_cost_eur: number; weekly_cost_eur: number };

const asToken = (secret: string, path: string, method = "GET") =>
  fetch(`${apiURL()}/api/v1/${path}`, { method, headers: { Authorization: `Bearer ${secret}`, "Content-Type": "application/json" }, body: method === "GET" ? undefined : "{}" });

test.beforeEach(async ({ page }) => {
  await signIn(page);
});

test("settings hub offers every admin tab and switches between them", async ({ page }) => {
  await page.goto("/settings");
  await expect(page.getByRole("heading", { name: "Settings", level: 1 })).toBeVisible();
  const tabs = page.getByRole("group", { name: "Settings", exact: true });
  await expect(tabs.getByRole("button", { name: "Charter", exact: true })).toHaveAttribute("aria-pressed", "true");

  const panels: [string, string][] = [
    ["Capacity and limits", "Organization spend caps"],
    ["Repositories", "Repositories"],
    ["Announcements", "Announcements"],
    ["Schedules", "Schedules"],
    ["Tokens", "API tokens"],
    ["Webhooks", "Outbound webhooks"],
    ["Retention", "Data retention"],
    ["About", "About"],
    ["Charter", "Charter"],
  ];
  for (const [tab, heading] of panels) {
    await tabs.getByRole("button", { name: tab, exact: true }).click();
    await expect(tabs.getByRole("button", { name: tab, exact: true })).toHaveAttribute("aria-pressed", "true");
    await expect(page.getByRole("heading", { name: heading, exact: true })).toBeVisible();
  }
});

test("charter edits are saved and shown to agents", async ({ page }) => {
  const text = `Charter ${uniqueID("rule")}: agents never push to main.`;
  await openSettings(page, "Charter");
  const editor = page.getByLabel("Markdown");
  await expect(editor).toBeVisible();
  await expect(page.getByRole("button", { name: "Save charter" })).toBeDisabled();

  await editor.fill(text);
  await expect(page.getByRole("button", { name: "Discard changes" })).toBeEnabled();
  await page.getByRole("button", { name: "Save charter" }).click();
  await expect(notice(page, "Charter saved.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Save charter" })).toBeDisabled();
  await expect.poll(async () => (await owner<{ text: string }>("charter")).text).toBe(text);

  await page.reload();
  await page.getByRole("group", { name: "Settings", exact: true }).getByRole("button", { name: "Charter", exact: true }).click();
  await expect(page.getByLabel("Markdown")).toHaveValue(text);
});

test("capacity limits and spend caps save and persist", async ({ page }) => {
  const before = await owner<Capacity>("capacity");
  const spend = await owner<SpendLimits>("limits");
  const provider = uniqueID("prov");
  try {
    await openSettings(page, "Capacity and limits");
    await expect(page.getByRole("heading", { name: "Capacity settings" })).toBeVisible();

    await page.getByLabel("Global maximum agents").fill("77");
    await page.getByRole("button", { name: "Add limit" }).click();
    const row = page.locator(".capacity-row").last();
    await row.getByLabel("Provider", { exact: true }).fill(provider);
    await row.getByLabel("Max concurrent").fill("3");
    await row.getByLabel("Requests / min").fill("120");
    await page.getByRole("button", { name: "Save settings" }).click();
    await expect(notice(page, "Saved the capacity settings.")).toBeVisible();

    await expect.poll(async () => {
      const saved = await owner<Capacity>("capacity");
      return [saved.global_max_agents, saved.limits?.find((limit) => limit.provider === provider)?.max_concurrency];
    }).toEqual([77, 3]);

    await page.getByLabel("Daily cap (EUR, 0 = off)").fill("900");
    await page.getByLabel("Weekly cap (EUR, 0 = off)").fill("4500");
    await page.getByRole("button", { name: "Save caps" }).click();
    await expect(notice(page, "Saved the spend caps.")).toBeVisible();
    await expect.poll(async () => {
      const saved = await owner<SpendLimits>("limits");
      return [saved.daily_cost_eur, saved.weekly_cost_eur];
    }).toEqual([900, 4500]);
    await expect(page.getByRole("progressbar", { name: "daily spend" })).toBeVisible();

    await page.reload();
    await page.getByRole("group", { name: "Settings", exact: true }).getByRole("button", { name: "Capacity and limits" }).click();
    await expect(page.getByLabel("Global maximum agents")).toHaveValue("77");
    await expect(page.locator(".capacity-row").last().getByLabel("Provider", { exact: true })).toHaveValue(provider);
  } finally {
    await owner("capacity", "PUT", { global_max_agents: before.global_max_agents, limits: before.limits ?? [] });
    await owner("limits", "PUT", { daily_cost_eur: spend.daily_cost_eur, weekly_cost_eur: spend.weekly_cost_eur });
  }
});

test("announcements publish to agents and can be deleted", async ({ page }) => {
  const text = uniqueID("Freeze deploys");
  await openSettings(page, "Announcements");
  await page.getByLabel("Text", { exact: true }).fill(text);
  await page.getByRole("button", { name: "Publish announcement" }).click();
  await expect(notice(page, /^Published AN-\d+ to \d+ agent threads?\.$/)).toBeVisible();

  const active = page.getByRole("region", { name: "Active announcements" });
  const item = active.locator("article").filter({ hasText: text });
  await expect(item).toContainText("every agent · no expiry");
  await expect.poll(async () => {
    const { announcements } = await owner<{ announcements: { text: string }[] }>("announcements");
    return announcements.some((announcement) => announcement.text === text);
  }).toBe(true);

  await item.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("alertdialog", { name: /^Delete announcement AN-\d+\?$/ }).getByRole("button", { name: "Delete" }).click();
  await expect(item).toHaveCount(0);
  await expect.poll(async () => {
    const { announcements } = await owner<{ announcements: { text: string }[] }>("announcements");
    return announcements.some((announcement) => announcement.text === text);
  }).toBe(false);
});

test("schedules preview runs, toggle and delete", async ({ page }) => {
  const name = uniqueID("weekly");
  await openSettings(page, "Schedules");
  await page.getByLabel("Name", { exact: true }).fill(name);
  await page.getByLabel("Brief source").selectOption("markdown");
  await page.getByLabel("Brief document").fill(`---\ntitle: "${name}"\n---\nScheduled by the browser suite.\n`);
  await expect(page.getByRole("status", { name: "Next runs" }).getByRole("listitem")).toHaveCount(5);
  await page.getByRole("button", { name: "Create schedule" }).click();
  await expect(notice(page, `Created schedule ${name}.`)).toBeVisible();

  const list = page.getByRole("region", { name: "Schedules", exact: true });
  const item = list.locator("article").filter({ hasText: name });
  await expect(item).toContainText("enabled");
  await expect(item).toContainText("0 9 * * 1");

  await item.getByRole("button", { name: "Disable" }).click();
  await expect(notice(page, `Disabled schedule ${name}.`)).toBeVisible();
  await expect(item.getByRole("button", { name: "Enable" })).toBeVisible();
  await expect.poll(async () => {
    const { schedules } = await owner<{ schedules: { name: string; enabled: boolean }[] }>("schedules");
    return schedules.find((schedule) => schedule.name === name)?.enabled;
  }).toBe(false);

  await item.getByRole("button", { name: "Runs" }).click();
  await expect(item.getByText("No runs yet.")).toBeVisible();

  await item.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("alertdialog", { name: `Delete schedule ${name}?` }).getByRole("button", { name: "Delete" }).click();
  await expect(item).toHaveCount(0);
});

test("repositories register, edit and remove", async ({ page }) => {
  const name = `SigmaUno/${uniqueID("repo")}`;
  try {
    await openSettings(page, "Repositories");
    await page.getByRole("button", { name: "New repository" }).click();
    await page.getByLabel("Name", { exact: true }).fill(name);
    await page.getByLabel("Default branch").fill("main");
    await page.getByLabel("Protected branches").fill("main, release/*");
    await page.getByRole("button", { name: "Add repository" }).click();

    const row = page.getByRole("row").filter({ hasText: name });
    await expect(row).toContainText("protected main, release/*");
    await expect.poll(async () => (await owner<{ repos: { name: string }[] }>("repos")).repos.some((repo) => repo.name === name)).toBe(true);

    await row.getByRole("button", { name: "Edit" }).click();
    await page.getByLabel("Default branch").fill("develop");
    await page.getByRole("button", { name: "Save repository" }).click();
    await expect(row).toContainText("develop");

    page.once("dialog", (dialog) => void dialog.accept());
    await row.getByRole("button", { name: "Remove" }).click();
    await expect(row).toHaveCount(0);
    await expect.poll(async () => (await owner<{ repos: { name: string }[] }>("repos")).repos.some((repo) => repo.name === name)).toBe(false);
  } finally {
    await fetch(`${apiURL()}/api/v1/repos/${name}`, { method: "DELETE", headers: { Authorization: `Bearer ${ownerToken()}` } });
  }
});

test("api tokens show their secret once and revoke immediately", async ({ page }) => {
  const name = uniqueID("viewer");
  await openSettings(page, "Tokens");
  await page.getByLabel("Name", { exact: true }).fill(name);
  await page.getByLabel("Scope").selectOption("viewer");
  await page.getByRole("button", { name: "Create token" }).click();
  await expect(notice(page, /^Created token K-\d+\.$/)).toBeVisible();

  const secret = (await page.getByText(/^ssu_/).textContent())!.trim();
  expect((await asToken(secret, "me")).status).toBe(200);
  expect((await asToken(secret, "pause-all", "POST")).status).toBe(403);

  await page.getByRole("button", { name: "Dismiss secret" }).click();
  await expect(page.getByText(secret)).toHaveCount(0);
  const row = page.getByRole("row").filter({ hasText: name });
  await expect(row).toContainText("viewer");
  await expect(row.getByText("active", { exact: true })).toBeVisible();

  await row.getByRole("button", { name: `Revoke token ${name}` }).click();
  await page.getByRole("alertdialog", { name: /^Revoke token K-\d+\?$/ }).getByRole("button", { name: "Revoke" }).click();
  await expect(row.getByText("revoked", { exact: true })).toBeVisible();
  await expect.poll(async () => (await asToken(secret, "me")).status).toBe(401);
});

test("outbound webhooks are created with a one-time secret and deleted", async ({ page }) => {
  const host = `${uniqueID("hooks")}.example.com`;
  await openSettings(page, "Webhooks");
  await page.getByLabel("Receiver URL").fill(`https://${host}/subspace`);
  await page.getByLabel("Events (comma separated)").fill("task.updated, decision.*");
  await page.getByRole("button", { name: "Create webhook" }).click();
  await expect(notice(page, /^Created webhook \S+\.$/)).toBeVisible();
  await expect(page.getByText("Save this signing secret now.")).toBeVisible();
  await page.getByRole("button", { name: "Dismiss secret" }).click();
  await expect(page.getByText("Save this signing secret now.")).toHaveCount(0);

  const row = page.getByRole("row").filter({ hasText: host });
  await expect(row).toContainText("task.updated, decision.*");
  await expect(row.getByRole("button", { name: /^Deliveries of / })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByRole("heading", { name: /^Deliveries of / })).toBeVisible();
  await expect(page.getByText("No deliveries.")).toBeVisible();

  await row.getByRole("button", { name: /^Delete / }).click();
  await page.getByRole("alertdialog", { name: /^Delete webhook \S+\?$/ }).getByRole("button", { name: "Delete" }).click();
  await expect(row).toHaveCount(0);
});

test("retention runs an archive pass and verifies the archive", async ({ page }) => {
  await openSettings(page, "Retention");
  await expect(page.getByRole("heading", { name: "Data retention" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Segments" })).toBeVisible();

  await page.getByRole("button", { name: "Run now" }).click();
  await expect(notice(page, "Archive run finished.")).toBeVisible();
  await expect(page.getByText(/^Last run /)).toBeVisible();

  await page.getByRole("button", { name: "Verify archive" }).click();
  await expect(page.getByRole("status").filter({ hasText: /^Archive verified: / })).toBeVisible();
});
