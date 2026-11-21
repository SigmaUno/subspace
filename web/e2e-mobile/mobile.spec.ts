import { randomBytes } from "node:crypto";
import { expect, test, type Locator, type Page } from "@playwright/test";

const api = () => process.env.MOBILE_E2E_API_URL!;
const web = () => process.env.MOBILE_E2E_WEB_URL!;
const token = () => process.env.MOBILE_E2E_TOKEN!;
const unique = (prefix: string) => `${prefix}-${randomBytes(4).toString("hex")}`;

async function owner<T = unknown>(path: string, method = "GET", body?: unknown): Promise<T> {
  const response = await fetch(`${api()}/api/v1/${path}`, {
    method,
    headers: { Authorization: `Bearer ${token()}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) throw new Error(`${method} ${path}: HTTP ${response.status} ${await response.text()}`);
  return response.json() as Promise<T>;
}

class Leader {
  private session = "";
  private sequence = 0;

  constructor(private readonly secret: string) {}

  private async post(message: Record<string, unknown>) {
    const headers: Record<string, string> = {
      Authorization: `Bearer ${this.secret}`,
      Accept: "application/json",
      "Content-Type": "application/json",
      "MCP-Protocol-Version": "2025-06-18",
    };
    if (this.session) headers["Mcp-Session-Id"] = this.session;
    const response = await fetch(`${api()}/mcp`, { method: "POST", headers, body: JSON.stringify({ jsonrpc: "2.0", ...message }) });
    if (!response.ok) throw new Error(`MCP: HTTP ${response.status} ${await response.text()}`);
    this.session = response.headers.get("mcp-session-id") ?? this.session;
    return response.status === 202 ? null : await response.json() as { result?: { isError?: boolean; structuredContent?: unknown }; error?: { message: string } };
  }

  async connect() {
    await this.post({ id: ++this.sequence, method: "initialize", params: { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "mobile-e2e", version: "1" } } });
    await this.post({ method: "notifications/initialized" });
    return this;
  }

  async call<T>(name: string, args: Record<string, unknown>) {
    const reply = await this.post({ id: ++this.sequence, method: "tools/call", params: { name, arguments: args } });
    if (!reply?.result || reply.error || reply.result.isError) throw new Error(`MCP ${name}: ${JSON.stringify(reply)}`);
    return reply.result.structuredContent as T;
  }
}

type Decision = { id: string };

async function seed() {
  const title = unique("Brief");
  const brief = await owner<{ id: string }>("briefs", "POST", {
    title, body: "Mobile smoke brief.", repos: ["SigmaUno/mobile"], max_cost_eur: 10, max_parallel_agents: 2, allowed_providers: [],
  });
  const id = unique("lead");
  const { connect } = await owner<{ connect: { token: string } }>("agents", "POST", { id, role: "leader", kind: "other", model: "e2e", provider: "openai" });
  const leader = await new Leader(connect.token).connect();
  const question = unique("Which database");
  await owner<{ decisions: Decision[] }>("decisions").then(({ decisions }) =>
    Promise.all((decisions ?? []).map((item) => owner(`decisions/${encodeURIComponent(item.id)}`, "POST", { action: "skip", snooze: "1h" }))));
  await leader.call<unknown>("ask_owner", { question, options: ["sqlite", "postgres"] });
  const { plan } = await leader.call<{ plan: { id: string } }>("propose_plan", {
    brief_id: brief.id,
    note: "One task",
    tasks: [{ ref: "api", title: `${title} API`, spec: "Add the API.", acceptance_criteria: ["tests pass"], repo: "SigmaUno/mobile" }],
  });
  return { title, brief, plan, question, leader: id };
}

async function signIn(page: Page) {
  await page.context().addCookies([{ name: "subspace_owner", value: token(), url: web(), httpOnly: true, sameSite: "Strict" }]);
}

async function settled(page: Page) {
  await expect(page.locator("main").first()).toBeVisible();
  await expect(page.getByText("Loading…")).toHaveCount(0);
}

async function overflow(page: Page) {
  return page.evaluate(() => {
    const root = document.scrollingElement!;
    const wide = [...document.body.querySelectorAll<HTMLElement>("*")]
      .filter((element) => element.getBoundingClientRect().right > innerWidth + 1 && !element.closest(".board, .table-wrap, .virtual-scroll, .graph-scroll, .arch-canvas, .view-tabs, pre, [hidden]"))
      .slice(0, 3)
      .map((element) => `${element.tagName}.${element.className}`);
    return { overflow: root.scrollWidth - innerWidth, wide };
  });
}

async function target(locator: Locator, minimum = 44) {
  const box = await locator.boundingBox();
  expect(box, "target is rendered").not.toBeNull();
  expect(box!.height, "touch target height").toBeGreaterThanOrEqual(minimum);
  expect(box!.width, "touch target width").toBeGreaterThanOrEqual(minimum);
}

const mobile = (name: string) => name === "mobile";

test.describe("owner workspace", () => {
  let data: Awaited<ReturnType<typeof seed>>;

  test.beforeEach(async ({ page }) => {
    data = await seed();
    await signIn(page);
  });

  test("is installable without caching authenticated responses", async ({ page, request }) => {
    await page.goto("/login");
    await expect(page.locator('link[rel="manifest"]')).toHaveAttribute("href", "/manifest.webmanifest");
    await expect(page.locator('meta[name="viewport"]')).toHaveAttribute("content", /width=device-width.*viewport-fit=cover/);
    await expect(page.locator('meta[name="theme-color"]')).toHaveAttribute("content", /^#[0-9a-f]{6}$/i);
    await expect(page.locator('link[rel="apple-touch-icon"]')).toHaveCount(1);

    const manifest = await (await request.get("/manifest.webmanifest")).json();
    expect(manifest).toMatchObject({ name: "Subspace", start_url: "/", display: "standalone" });
    expect(manifest.icons.map((icon: { sizes: string; purpose: string }) => `${icon.sizes}:${icon.purpose}`)).toEqual(expect.arrayContaining(["192x192:any", "512x512:any", "512x512:maskable"]));
    for (const icon of manifest.icons as { src: string }[]) {
      const response = await request.get(icon.src);
      expect(response.status(), icon.src).toBe(200);
      expect(response.headers()["content-type"]).toBe("image/png");
    }

    await page.goto("/");
    await settled(page);
    expect(await page.evaluate(async () => (await navigator.serviceWorker.getRegistrations()).length)).toBe(0);
  });

  test("no screen scrolls horizontally", async ({ page }, info) => {
    const paths = ["/", `/briefs/${data.brief.id}`, `/briefs/${data.brief.id}/architecture`, "/briefs", "/agents", "/bots", "/plans", "/questions",
      "/capacity", "/charter", "/usage", "/files", "/instructions", "/audit", "/mergequeue", "/prompts", "/hillclimb", `/agents/${data.leader}`];
    const height = page.viewportSize()!.height;
    const widths = mobile(info.project.name) ? [360, 390, 430] : [page.viewportSize()!.width];
    for (const width of widths) {
      await page.setViewportSize({ width, height });
      for (const path of paths) {
        await page.goto(path);
        await settled(page);
        expect(await overflow(page), `${path} at ${width}px`).toEqual({ overflow: 0, wide: [] });
      }
    }
  });

  test("navigation adapts to the viewport", async ({ page }, info) => {
    await page.goto("/");
    await settled(page);
    const tabs = page.getByRole("navigation", { name: "Primary" });
    const top = page.getByRole("navigation", { name: "Workspace" });
    if (!mobile(info.project.name)) {
      await expect(tabs).toBeHidden();
      await expect(top).toBeVisible();
      return;
    }
    await expect(top).toBeHidden();
    await expect(tabs).toBeVisible();
    const viewport = page.viewportSize()!;
    const bar = (await tabs.boundingBox())!;
    expect(bar.y + bar.height).toBeCloseTo(viewport.height, 0);
    for (const control of await tabs.locator("a, button").all()) await target(control);

    await tabs.getByRole("link", { name: "Briefs" }).click();
    await expect(page).toHaveURL(/\/briefs$/);
    await tabs.getByRole("button", { name: "More" }).click();
    const more = page.getByRole("dialog", { name: "More" });
    await expect(more.getByRole("link", { name: "Charter" })).toBeVisible();
    await more.getByRole("link", { name: "Charter" }).click();
    await expect(page).toHaveURL(/\/charter$/);
    await expect(more).toBeHidden();
    await expect(page.getByRole("button", { name: "Pause all" })).toBeVisible();
    await target(page.getByRole("button", { name: "Pause all" }), 44);
  });

  test("owner answers a question and approves a plan from the inbox", async ({ page }, info) => {
    await page.goto("/");
    await settled(page);
    const inbox = page.getByRole("complementary", { name: "Decision inbox" });
    await expect(inbox).toBeVisible();

    const plan = inbox.getByRole("heading", { name: `Approve plan ${data.plan.id} for ${data.brief.id}` });
    await expect(plan).toBeVisible();
    const approve = inbox.getByRole("button", { name: /^Approve/ });
    if (mobile(info.project.name)) {
      for (const control of await inbox.locator(".decision-bar button").all()) await target(control, 44);
      const tabs = (await page.getByRole("navigation", { name: "Primary" }).boundingBox())!;
      const bar = (await inbox.locator(".decision-bar").boundingBox())!;
      expect(bar.y + bar.height).toBeLessThanOrEqual(tabs.y + 1);
      await expect(approve).toBeInViewport({ ratio: 1 });
      expect((await overflow(page)).overflow).toBe(0);
    }
    await approve.click();
    await expect.poll(async () => (await owner<{ status: string }>(`plans/${data.plan.id}`)).status).toBe("approved");

    await expect(inbox.getByRole("heading", { name: new RegExp(data.question) })).toBeVisible();
    await inbox.getByLabel("Answer").fill("sqlite");
    await inbox.getByRole("button", { name: /^Answer/ }).click();
    await expect.poll(async () => {
      const { questions } = await owner<{ questions: { question: string; status: string; answer: string | null }[] }>("questions");
      return questions.find((item) => item.question === data.question);
    }).toMatchObject({ status: "answered", answer: "sqlite" });
  });

  test("owner chats with the architect and replies in a thread", async ({ page }, info) => {
    await page.goto(`/briefs/${data.brief.id}`);
    await settled(page);
    await page.getByRole("button", { name: "Owner chat" }).click();
    const chat = page.getByRole("region", { name: "Owner chat" });
    await expect(chat).toBeVisible();
    const message = unique("ping");
    await chat.getByLabel("Message").fill(message);
    const send = chat.getByRole("button", { name: "Send message" });
    if (mobile(info.project.name)) {
      const viewport = page.viewportSize()!;
      expect(await chat.boundingBox()).toMatchObject({ x: 0, y: 0, width: viewport.width, height: viewport.height });
      await expect(send).toBeInViewport({ ratio: 1 });
      await target(send, 44);
    }
    await send.click();
    await expect(chat.getByRole("log", { name: "Conversation" }).getByText(message)).toBeVisible();
    await chat.getByRole("button", { name: "Close chat" }).click();
    await expect(chat).toBeHidden();

    await page.getByRole("button", { name: "Brief thread" }).click();
    const thread = page.getByRole("complementary", { name: "Thread" });
    await expect(thread).toBeVisible();
    const reply = unique("reply");
    await thread.getByLabel("Owner reply").fill(reply);
    const submit = thread.getByRole("button", { name: "Send reply" });
    if (mobile(info.project.name)) {
      const viewport = page.viewportSize()!;
      expect(await thread.boundingBox()).toMatchObject({ x: 0, y: 0, width: viewport.width, height: viewport.height });
      await expect(submit).toBeInViewport({ ratio: 1 });
    }
    await submit.click();
    await expect(thread.getByRole("log", { name: "Messages" }).getByText(reply)).toBeVisible();
    expect((await overflow(page)).overflow).toBe(0);
  });
});
