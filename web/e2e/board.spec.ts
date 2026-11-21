import { expect, helperFor, owner, seedBrief, signIn, test, uniqueID, type Plan, type Task } from "./subspace";

test("owner approves a seeded plan from the decision inbox", async ({ page }) => {
  const { brief, plan } = await seedBrief();
  await signIn(page);
  await page.goto(`/briefs/${brief.id}`);

  const inbox = page.getByRole("complementary", { name: "Decision inbox" });
  await expect(inbox.getByRole("heading", { name: `Approve plan ${plan.id} for ${brief.id}` })).toBeVisible();
  const backlog = page.getByRole("region", { name: "Backlog", exact: true });
  await expect(backlog.getByRole("button", { name: `${brief.title} API` })).toBeVisible();

  await inbox.getByRole("button", { name: /^Approve/ }).click();
  await expect(inbox.getByText("No pending decisions.")).toBeVisible();
  await expect.poll(async () => (await owner<Plan>(`plans/${plan.id}`)).status).toBe("approved");

  const ready = page.getByRole("region", { name: "Ready", exact: true });
  await expect(ready.getByRole("button", { name: `${brief.title} API` })).toBeVisible();
  await expect(ready.getByRole("button", { name: `${brief.title} UI` })).toBeVisible();
  await expect(backlog.getByRole("listitem")).toHaveCount(0);
  await expect(page.getByText(`${brief.id} · open · plan approved`)).toBeVisible();
});

test("board follows tasks as agents move them over MCP", async ({ page }) => {
  const seeded = await seedBrief();
  const { brief, plan, worker } = seeded;
  const leaderCLI = helperFor(seeded.leader);
  const workerCLI = helperFor(worker);
  await owner(`plans/${plan.id}/approve`, "POST", { note: "" });
  const task = plan.tasks.find((item) => item.title.endsWith(" API"))!;
  const card = (column: string) => page.getByRole("region", { name: column, exact: true }).getByRole("button", { name: task.title });

  await signIn(page);
  await page.goto(`/briefs/${brief.id}`);
  await expect(card("Ready")).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "Live" })).toBeVisible();

  await leaderCLI.call("assign", { task_id: task.id, agent_id: worker.id });
  await expect(card("Assigned")).toBeVisible();
  await expect(card("Ready")).toHaveCount(0);

  await workerCLI.call("claim_task", { task_id: task.id });
  await workerCLI.call("update_status", { task_id: task.id, status: "in_progress", progress_pct: 60, note: "halfway" });
  await expect(card("In progress")).toBeVisible();
  const inProgress = page.getByRole("region", { name: "In progress", exact: true }).getByRole("listitem");
  await expect(inProgress.filter({ hasText: task.title })).toContainText("60%");
  await expect(inProgress.filter({ hasText: task.title }).getByRole("button", { name: worker.id })).toBeVisible();

  await workerCLI.call("submit", { task_id: task.id, pr_url: "https://github.com/SigmaUno/e2e/pull/1", summary: "done" });
  await expect(card("Submitted")).toBeVisible();
  await expect(card("In progress")).toHaveCount(0);
  await expect.poll(async () => (await owner<Task>(`tasks/${task.id}`)).status).toBe("submitted");
});

test("owner answers an agent question from the decision inbox", async ({ page }) => {
  const seeded = await seedBrief();
  const { brief, plan, worker } = seeded;
  const leaderCLI = helperFor(seeded.leader);
  const workerCLI = helperFor(worker);
  await owner(`plans/${plan.id}/approve`, "POST", { note: "" });
  const task = plan.tasks.find((item) => item.title.endsWith(" API"))!;
  await leaderCLI.call("assign", { task_id: task.id, agent_id: worker.id });
  await workerCLI.call("claim_task", { task_id: task.id });

  const question = uniqueID("Which database");
  const { question: asked } = await workerCLI.call<{ question: { id: string } }>("ask_owner", {
    task_id: task.id,
    question,
    options: ["sqlite", "postgres"],
  });

  await signIn(page);
  await page.goto(`/briefs/${brief.id}`);
  const inbox = page.getByRole("complementary", { name: "Decision inbox" });
  await expect(inbox.getByRole("heading", { name: question })).toBeVisible();
  await inbox.getByLabel("Answer", { exact: true }).fill("postgres");
  await inbox.getByRole("button", { name: /^Answer/ }).click();
  await expect(inbox.getByText("No pending decisions.")).toBeVisible();

  await expect.poll(async () => {
    const { questions } = await owner<{ questions: { id: string; answer: string | null }[] }>("questions?status=answered");
    return questions.find((item) => item.id === asked.id)?.answer;
  }).toBe("postgres");
  await expect.poll(async () => {
    const { messages } = await workerCLI.call<{ messages: { kind: string; body: string }[] }>("read_thread", { thread: `task:${task.id}` });
    return messages.some((message) => message.kind === "answer" && message.body.includes("postgres"));
  }).toBe(true);
});
