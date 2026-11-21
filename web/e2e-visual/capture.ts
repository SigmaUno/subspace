import { mkdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { chromium, type Page } from "playwright-core";

const web = process.env.VISUAL_WEB_URL ?? "http://127.0.0.1:3000";
const api = process.env.VISUAL_API_URL ?? "http://127.0.0.1:8080";
const token = process.env.VISUAL_OWNER_TOKEN;
const out = process.env.VISUAL_OUT ?? path.join(os.tmpdir(), "subspace-visual");
const only = process.env.VISUAL_ONLY?.split(",");

const viewports = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "tablet", width: 768, height: 1024 },
  { name: "mobile", width: 390, height: 844 },
];
const themes = ["light", "dark"] as const;

if (!token) throw new Error("VISUAL_OWNER_TOKEN is required");

async function owner<T>(route: string): Promise<T> {
  const response = await fetch(`${api}/api/v1/${route}`, { headers: { Authorization: `Bearer ${token}` } });
  if (!response.ok) throw new Error(`GET ${route}: HTTP ${response.status}`);
  return response.json() as Promise<T>;
}

type Screen = { name: string; path: string; prepare?: (page: Page, narrow: boolean) => Promise<void> };

async function screens(): Promise<Screen[]> {
  const [{ briefs }, { agents }, { tasks }] = await Promise.all([
    owner<{ briefs: { id: string }[] }>("briefs"),
    owner<{ agents: { id: string; role: string }[] }>("agents"),
    owner<{ tasks: { id: string; status: string }[] }>("tasks?limit=200"),
  ]);
  const brief = briefs[0]?.id;
  const agent = (agents.find((a) => a.role === "worker") ?? agents[0])?.id;
  const task = (tasks.find((t) => t.status === "in_progress") ?? tasks[0])?.id;
  if (!brief || !agent || !task) throw new Error("seed the server first: make demo");
  const inbox = async (page: Page, narrow: boolean) => {
    await page.getByRole("button", { name: narrow ? /^Inbox/ : /^Decisions/ }).click();
  };
  return [
    { name: "overview", path: "/" },
    { name: "board", path: "/briefs" },
    { name: "brief", path: `/briefs/${brief}` },
    { name: "task", path: `/tasks/${task}` },
    { name: "agents", path: "/agents" },
    { name: "agent", path: `/agents/${agent}` },
    { name: "decisions", path: "/briefs", prepare: inbox },
    { name: "chat", path: `/briefs/${brief}`, prepare: async (page) => { await page.getByRole("button", { name: "Owner chat" }).click(); } },
    { name: "plans", path: "/plans" },
    { name: "questions", path: "/questions" },
    { name: "usage", path: "/usage" },
    { name: "trends", path: "/trends" },
    { name: "settings", path: "/settings" },
    { name: "graph", path: `/briefs/${brief}/graph` },
    { name: "replay", path: `/briefs/${brief}/replay` },
    { name: "knowledge", path: "/knowledge" },
  ];
}

async function settle(page: Page) {
  await page.waitForLoadState("networkidle").catch(() => undefined);
  await page.waitForTimeout(600);
}

async function main() {
  const list = (await screens()).filter((screen) => !only || only.includes(screen.name));
  await mkdir(out, { recursive: true });
  const browser = await chromium.launch();
  const defects: string[] = [];
  try {
    for (const theme of themes) {
      for (const viewport of viewports) {
        const context = await browser.newContext({
          viewport: { width: viewport.width, height: viewport.height },
          colorScheme: theme,
          isMobile: viewport.name === "mobile",
          hasTouch: viewport.name === "mobile",
        });
        await context.addCookies([{ name: "subspace_owner", value: token!, url: web, httpOnly: true, sameSite: "Strict" }]);
        await context.addInitScript((value) => {
          document.documentElement.dataset.theme = value;
          new MutationObserver(() => {
            if (document.documentElement.dataset.theme !== value) document.documentElement.dataset.theme = value;
          }).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
        }, theme);
        const page = await context.newPage();
        for (const screen of list) {
          await page.goto(new URL(screen.path, web).toString());
          await settle(page);
          await screen.prepare?.(page, viewport.name === "mobile");
          await settle(page);
          const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
          if (overflow > 0) defects.push(`${screen.name} ${viewport.name} ${theme}: horizontal overflow ${overflow}px`);
          const file = path.join(out, `${screen.name}-${viewport.name}-${theme}.png`);
          await page.screenshot({ path: file, fullPage: true });
          console.log(file);
        }
        await context.close();
      }
    }
  } finally {
    await browser.close();
  }
  for (const defect of defects) console.warn(defect);
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
