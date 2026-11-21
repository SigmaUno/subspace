import { expect, owner, seedBrief, signIn, test, type Plan } from "./subspace";

test("decision inbox works from the keyboard: skip, approve, review skipped, reject", async ({ page }) => {
  const [one, two] = [await seedBrief(), await seedBrief()];
  const title = (seed: { brief: { id: string }; plan: Plan }) => `Approve plan ${seed.plan.id} for ${seed.brief.id}`;
  await signIn(page);
  await page.goto(`/briefs/${one.brief.id}`);

  const inbox = page.getByRole("complementary", { name: "Decision inbox" });
  const heading = inbox.getByRole("heading", { level: 3 });
  await expect(heading).toHaveText(/^Approve plan /);
  const first = (await heading.textContent()) === title(one) ? one : two;
  const second = first === one ? two : one;
  await expect(heading).toBeFocused();

  await page.keyboard.press("s");
  await expect(heading).toHaveText(title(second));
  await expect(heading).toBeFocused();

  await page.keyboard.press("a");
  await expect.poll(async () => (await owner<Plan>(`plans/${second.plan.id}`)).status).toBe("approved");
  await expect(inbox.getByText("No unskipped decisions.")).toBeVisible();
  expect((await owner<Plan>(`plans/${first.plan.id}`)).status).toBe("proposed");

  await inbox.getByRole("button", { name: "Review skipped" }).click();
  await expect(heading).toHaveText(title(first));
  await expect(heading).toBeFocused();

  await page.keyboard.press("r");
  await expect(inbox.getByLabel("Note or rejection reason")).toBeFocused();
  await inbox.getByLabel("Note or rejection reason").fill("scope too broad");
  await inbox.getByRole("button", { name: /^Reject/ }).focus();
  await page.keyboard.press("r");

  await expect.poll(async () => (await owner<Plan>(`plans/${first.plan.id}`)).status).toBe("rejected");
  await expect(inbox.getByText("No pending decisions.")).toBeVisible();
  await expect(inbox.getByRole("button", { name: "Review skipped" })).toHaveCount(0);
});
