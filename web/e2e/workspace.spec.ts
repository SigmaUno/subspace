import { readFile } from "node:fs/promises";
import { apiURL, expect, owner, register, signIn, test, uniqueID } from "./subspace";

type Message = { author_id: string; author_role: string; body: string };

test("owner replies in an agent thread and the agent reads it", async ({ page }) => {
  const worker = await register("chat", "worker", "codex", "openai");
  await worker.call("add_note", { body: "Agent checking in." });

  await signIn(page);
  await page.goto("/agents");
  await page.getByRole("listitem").filter({ has: page.getByRole("link", { name: worker.id, exact: true }) }).getByRole("button", { name: "Thread" }).click();

  const thread = page.getByRole("complementary", { name: "Thread" });
  await expect(thread.getByText(`agent · ${worker.id}`)).toBeVisible();
  const log = thread.getByRole("log", { name: "Messages" });
  await expect(log.getByText("Agent checking in.")).toBeVisible();

  const reply = uniqueID("Owner reply");
  await thread.getByLabel("Owner reply").fill(reply);
  await thread.getByRole("button", { name: "Send reply" }).click();
  await expect(log.locator("article").filter({ hasText: reply })).toContainText("owner · message");
  await expect(thread.getByLabel("Owner reply")).toHaveValue("");

  await expect.poll(async () => {
    const { messages } = await worker.call<{ messages: Message[] }>("read_thread", { thread: "agent:me" });
    return messages.find((message) => message.body === reply)?.author_role;
  }).toBe("owner");

  await worker.call("add_note", { body: "Agent acknowledges." });
  await expect(log.getByText("Agent acknowledges.")).toBeVisible();
});

test("pause all stops agents and resume all releases them", async ({ page }) => {
  const worker = await register("pause", "worker", "claude-code", "anthropic");
  await signIn(page);
  await page.goto("/");
  await expect(page.getByRole("status").filter({ hasText: "Live" })).toBeVisible();

  await page.getByRole("button", { name: "Pause all" }).click();
  await expect(page.getByRole("status").filter({ hasText: "All agents paused" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Resume all" })).toBeVisible();
  expect((await owner<{ paused: boolean }>("state")).paused).toBe(true);
  expect((await (await fetch(`${apiURL()}/healthz`)).json()).paused).toBe(true);
  expect((await worker.call<{ paused: boolean }>("heartbeat")).paused).toBe(true);

  await page.reload();
  await expect(page.getByRole("status").filter({ hasText: "All agents paused" })).toBeVisible();

  await page.getByRole("button", { name: "Resume all" }).click();
  await expect(page.getByRole("button", { name: "Pause all" })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "All agents paused" })).toHaveCount(0);
  expect((await owner<{ paused: boolean }>("state")).paused).toBe(false);
  expect((await worker.call<{ paused: boolean }>("heartbeat")).paused).toBe(false);
});

test("usage page renders cost and provider panels", async ({ page }) => {
  await signIn(page);
  await page.goto("/usage");
  await expect(page.getByRole("heading", { name: "Usage", level: 1 })).toBeVisible();
  await expect(page.getByRole("link", { name: "Usage" })).toHaveAttribute("aria-current", "page");
  await expect(page.getByRole("heading", { name: "Provider availability" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Daily cost" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Brief budgets" })).toBeVisible();
  await expect(page.getByRole("main").getByRole("alert")).toHaveCount(0);

  await page.getByRole("group", { name: "Group cost by" }).getByRole("button", { name: "Model" }).click();
  await expect(page.getByRole("heading", { name: "Cost by model" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Model" })).toHaveAttribute("aria-pressed", "true");
});

test("files upload, list, download and delete round trip", async ({ page }) => {
  const agent = await register("files", "worker", "opencode", "deepseek");
  const name = `${uniqueID("notes")}.txt`;
  const content = `Subspace artifact ${name}\nline two\n`;

  await signIn(page);
  await page.goto("/files");
  await expect(page.getByRole("heading", { name: "Files", level: 1 })).toBeVisible();
  await page.getByRole("combobox", { name: "Agent", exact: true }).selectOption(agent.id);
  await expect(page.getByText(`Drop files here to upload to agent:${agent.id}`)).toBeVisible();
  await expect(page.getByText("No files in this scope.")).toBeVisible();

  await page.getByLabel("Files to upload").setInputFiles({ name, mimeType: "text/plain", buffer: Buffer.from(content) });
  const row = page.getByRole("row").filter({ hasText: name });
  await expect(row).toBeVisible();
  await expect(row).toContainText(`${Buffer.byteLength(content)} B`);
  await expect(page.getByRole("list", { name: "Uploads" })).toContainText("100%");

  const { artifacts } = await owner<{ artifacts: { name: string; uploaded_by: string }[] }>(`artifacts?scope=agent:${agent.id}`);
  expect(artifacts.map((artifact) => artifact.name)).toEqual([name]);

  const downloading = page.waitForEvent("download");
  await row.getByRole("link", { name: "Download" }).click();
  const download = await downloading;
  expect(download.suggestedFilename()).toBe(name);
  expect(await readFile(await download.path(), "utf8")).toBe(content);

  page.once("dialog", (dialog) => dialog.accept());
  await row.getByRole("button", { name: "Delete" }).click();
  await expect(row).toHaveCount(0);
  await expect(page.getByText("No files in this scope.")).toBeVisible();
});
