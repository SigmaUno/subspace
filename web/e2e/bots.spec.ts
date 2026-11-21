import { apiURL, expect, owner, signIn, test, uniqueID } from "./subspace";

type Connect = { token?: string; mcp_url: string; snippets: { claude_code: string } };

test("bot registration shows connect snippets once", async ({ page }) => {
  const id = uniqueID("bot");
  await signIn(page);
  await page.goto("/bots");
  await expect(page.getByRole("heading", { name: "Bots", level: 1 })).toBeVisible();

  await page.getByRole("button", { name: "Add bot", exact: true }).click();
  await page.getByLabel("Name", { exact: true }).fill(id);
  await page.getByLabel("Model", { exact: true }).fill("claude-sonnet-5-5");
  await page.getByRole("button", { name: "Add bot", exact: true }).click();

  const credentials = page.locator("section").filter({ has: page.getByRole("heading", { name: "Connect snippets" }) });
  await expect(credentials.getByText("Tokens are displayed once.")).toBeVisible();
  const secret = (label: string) => credentials.locator(".secret").filter({ hasText: label }).locator("pre");
  const token = (await secret("Agent token").innerText()).trim();
  expect(token).toMatch(/^ssp_/);
  await expect(secret("Join line")).toHaveText(`Join Subspace at ${apiURL()} as ${id}. Call get_instructions and follow them.`);
  await expect(secret("Claude Code")).toContainText(`${apiURL()}/mcp`);
  await expect(secret("Claude Code")).toContainText(`Bearer ${token}`);
  await expect(secret("Codex config.toml")).toContainText(token);
  await expect(secret("OpenCode opencode.json")).toContainText(token);
  await expect(page.getByRole("row").filter({ hasText: id })).toBeVisible();

  const stored = await owner<Connect>(`agents/${id}/connect`);
  expect(stored.token).toBeUndefined();
  expect(stored.snippets.claude_code).toContain("Bearer <token>");
  expect(stored.snippets.claude_code).not.toContain(token);

  await page.getByRole("button", { name: "Dismiss credentials" }).click();
  await expect(credentials).toHaveCount(0);
  await expect(page.getByText(token)).toHaveCount(0);

  await page.reload();
  await expect(page.getByRole("row").filter({ hasText: id })).toBeVisible();
  await expect(credentials).toHaveCount(0);
  await expect(page.getByText(token)).toHaveCount(0);
});
