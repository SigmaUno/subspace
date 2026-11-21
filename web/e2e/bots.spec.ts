import { apiURL, expect, notice, owner, signIn, test, uniqueID, type Brief } from "./subspace";

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

test("spawn form requests agents and kill stops one", async ({ page }) => {
  const brief = await owner<Brief>("briefs", "POST", {
    title: uniqueID("Spawn"), body: "Spawn from the Bots page.", repos: ["SigmaUno/e2e"], max_cost_eur: 50, max_parallel_agents: 5,
  });
  const legacy: string[] = [];
  page.on("request", (request) => { if (/\/bots\/[^/]+\/spawn$/.test(request.url())) legacy.push(request.url()); });
  await signIn(page);
  await page.goto("/bots");

  await page.getByRole("button", { name: "Spawn agents", exact: true }).click();
  const form = page.locator("form").filter({ has: page.getByRole("heading", { name: "Spawn agents" }) });
  await form.getByLabel("Brief").selectOption(brief.id);
  await form.getByLabel("Kind").selectOption("opencode-deepseek");
  await form.getByLabel("Model").fill("deepseek-chat");
  await form.getByLabel("Count").fill("2");
  await form.getByRole("button", { name: "Request spawn" }).click();

  const spawns = page.locator("section[aria-label='Spawns']");
  const rows = spawns.getByRole("row").filter({ hasText: `${brief.id} · fake` });
  await expect(rows).toHaveCount(2);
  const { spawns: created } = await owner<{ spawns: { agent_id: string; brief_id: string; status: string }[] }>(`spawns?limit=1000`);
  const agents = created.filter((spawn) => spawn.brief_id === brief.id);
  expect(agents).toHaveLength(2);
  expect(agents.every((spawn) => spawn.status === "spawned")).toBe(true);

  const target = agents[0].agent_id;
  const row = page.getByRole("region", { name: "Bots" }).getByRole("row").filter({ hasText: target });
  await row.getByRole("button", { name: "Kill", exact: true }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Kill", exact: true }).click();
  await expect(notice(page, `Killed ${target}: stopped.`)).toBeVisible();
  await expect(row.locator(".pill")).toHaveText("paused");
  expect((await owner<{ status: string }>(`agents/${target}`)).status).toBe("paused");
  expect(legacy).toEqual([]);
});
