import { expect, notice, owner, signIn, test, uniqueID } from "./subspace";

type Incident = { id: string; reason: string; ended_at: string | null };

const active = async () => (await owner<{ incidents: Incident[] | null }>("incident")).incidents?.find((item) => !item.ended_at);

test("owner declares an incident and ends it after confirming", async ({ page }) => {
  const reason = uniqueID("Leaked token");
  await signIn(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Incident", exact: true }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Declare an incident" })).toBeVisible();
  await expect(dialog.getByRole("button", { name: "Declare incident" })).toBeDisabled();
  await dialog.getByLabel("Reason").fill(reason);
  await expect(dialog.getByRole("checkbox", { name: /Pause everything/ })).toBeChecked();
  await expect(dialog.getByRole("checkbox", { name: /Revoke agents/ })).not.toBeChecked();
  await dialog.getByRole("button", { name: "Declare incident" }).click();

  await expect(dialog.getByText(/^Incident INC-\d+ · \d+ done, 0 failed$/)).toBeVisible();
  await expect(dialog.getByRole("listitem").filter({ hasText: "pause_all" })).toContainText("ok");
  await dialog.getByRole("button", { name: "Done" }).click();

  const banner = page.getByRole("alert").filter({ hasText: reason });
  await expect(banner).toContainText(/Incident INC-\d+ is active/);
  await expect(page.getByRole("status").filter({ hasText: "All agents paused" })).toContainText("incident INC-");
  expect((await owner<{ paused: boolean }>("state")).paused).toBe(true);
  expect((await active())?.reason).toBe(reason);

  await banner.getByRole("button", { name: "End incident" }).click();
  const confirm = page.getByRole("alertdialog", { name: /^End incident INC-\d+\?$/ });
  await confirm.getByRole("button", { name: "Cancel" }).click();
  await expect(confirm).toHaveCount(0);
  expect((await active())?.reason).toBe(reason);

  await banner.getByRole("button", { name: "End incident" }).click();
  await page.getByRole("alertdialog", { name: /^End incident INC-\d+\?$/ }).getByRole("button", { name: "End incident" }).click();
  await expect(notice(page, /^Ended incident INC-\d+\.$/)).toBeVisible();
  await expect(banner).toHaveCount(0);
  await expect(page.getByRole("status").filter({ hasText: "All agents paused" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Incident", exact: true })).toBeVisible();
  await expect.poll(async () => (await owner<{ paused: boolean }>("state")).paused).toBe(false);
  expect(await active()).toBeUndefined();
});
