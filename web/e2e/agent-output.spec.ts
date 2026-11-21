import { expect, helperFor, register, signIn, test } from "./subspace";

test("agent detail streams terminal output as the agent logs it", async ({ page }) => {
  const worker = await register("term", "worker", "opencode", "deepseek");
  const cli = helperFor(worker);
  await cli.call("log_output", { stream: "stdout", text: "compiling packages\nlinking binary" });

  await signIn(page);
  await page.goto(`/agents/${worker.id}`);
  const panel = page.getByRole("region", { name: `Live output of ${worker.id}` });
  const log = panel.getByRole("log", { name: `Output of ${worker.id}` });
  await expect(panel.getByRole("status").filter({ hasText: /^(Live|Connecting)$/ })).toHaveText("Live");
  await expect(log.getByText("compiling packages")).toBeVisible();
  await expect(log.getByText("linking binary")).toBeVisible();

  await cli.call("log_output", { stream: "stderr", text: "\u001b[31mwarning: slow disk\u001b[0m" });
  await expect(log.getByText("warning: slow disk")).toBeVisible();
  await expect(log.locator('[data-stream="stderr"]')).toHaveCount(1);
  await expect(log.getByText("\u001b[31m")).toHaveCount(0);

  await cli.call("log_output", { stream: "stdout", text: "tests passed" });
  await expect(log.getByText("tests passed")).toBeVisible();
  await expect(log.locator(".terminal-line")).toHaveCount(4);

  await panel.getByLabel("Search").fill("tests");
  await expect(panel.getByText("1 matching of 4 lines")).toBeVisible();
  await expect(log.locator(".terminal-line")).toHaveCount(1);
  await panel.getByLabel("Search").fill("");
  await expect(log.locator(".terminal-line")).toHaveCount(4);

  await panel.getByRole("button", { name: "Pause" }).click();
  await expect(panel.getByRole("button", { name: "Follow" })).toBeVisible();
  await expect(panel.getByText("Auto-scroll paused.")).toBeVisible();
  await panel.getByRole("button", { name: "Follow" }).click();
  await expect(panel.getByRole("button", { name: "Pause" })).toBeVisible();
});

test("latest output lines appear on the swarm overview activity tile", async ({ page }) => {
  const worker = await register("tile", "worker", "codex", "openai");
  await signIn(page);
  await page.goto("/");
  const tile = page.getByRole("region", { name: "Agent activity" });
  await expect(tile.getByRole("heading", { name: "Agent activity" })).toBeVisible();

  await helperFor(worker).call("log_output", { stream: "stdout", text: "running migration 7" });
  const entry = tile.getByRole("listitem").filter({ has: page.getByRole("link", { name: worker.id, exact: true }) });
  await expect(entry).toContainText("running migration 7");
  await entry.getByRole("link", { name: worker.id, exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/agents/${worker.id}$`));
  await expect(page.getByRole("log", { name: `Output of ${worker.id}` }).getByText("running migration 7")).toBeVisible();
});
