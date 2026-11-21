import { mkdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import AxeBuilder from "@axe-core/playwright";
import { chromium, type Page } from "playwright-core";

const web = process.env.VISUAL_WEB_URL ?? "http://127.0.0.1:3000";
const api = process.env.VISUAL_API_URL ?? "http://127.0.0.1:8080";
const token = process.env.VISUAL_OWNER_TOKEN;
const out = process.env.VISUAL_OUT ?? path.join(os.tmpdir(), "subspace-visual");
const only = process.env.VISUAL_ONLY?.split(",");
const settingsTabs = ["Charter", "Capacity and limits", "Repositories", "Instructions", "Playbooks", "Announcements", "Schedules", "Notifications", "Backups", "Digest", "Tokens", "Alerts", "Retention", "Webhooks", "Provider breakers", "About"];

const viewports = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "tablet", width: 768, height: 1024 },
  { name: "mobile", width: 390, height: 844 },
];
const themes = ["light"] as const;

if (!token) throw new Error("VISUAL_OWNER_TOKEN is required");

async function owner<T>(route: string): Promise<T> {
  const response = await fetch(`${api}/api/v1/${route}`, { headers: { Authorization: `Bearer ${token}` } });
  if (!response.ok) throw new Error(`GET ${route}: HTTP ${response.status}`);
  return response.json() as Promise<T>;
}

type Screen = { name: string; path: string; overlay?: boolean; prepare?: (page: Page, narrow: boolean) => Promise<void> };

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
    { name: "chat", path: `/briefs/${brief}`, prepare: async (page) => { await page.getByRole("button", { name: "Owner chat" }).click(); } },
    { name: "plans", path: "/plans" },
    { name: "questions", path: "/questions" },
    { name: "usage", path: "/usage" },
    { name: "trends", path: "/trends" },
    { name: "settings", path: "/settings" },
    { name: "graph", path: `/briefs/${brief}/graph` },
    { name: "replay", path: `/briefs/${brief}/replay` },
    { name: "knowledge", path: "/knowledge" },
    { name: "mission", path: "/mission" },
    { name: "costs", path: "/costs" },
    { name: "report", path: `/briefs/${brief}/report` },
    { name: "architecture", path: `/briefs/${brief}/architecture` },
    { name: "bots", path: "/bots" },
    { name: "help", path: "/help" },
    { name: "mergequeue", path: "/mergequeue" },
    { name: "audit", path: "/audit" },
    { name: "logs", path: "/logs" },
    { name: "hillclimb", path: "/hillclimb" },
    { name: "files", path: "/files" },
    { name: "instructions", path: "/instructions" },
    { name: "prompts", path: "/prompts" },
    { name: "capacity", path: "/capacity" },
    { name: "board-compact", path: `/briefs/${brief}`, prepare: async (page) => { await page.getByRole("button", { name: "Compact cards" }).click(); } },
    { name: "board-filtered", path: `/briefs/${brief}`, prepare: async (page) => { await page.getByRole("searchbox", { name: "Filter tasks" }).fill("zzzz-no-match"); } },
    { name: "incident", path: "/", overlay: true, prepare: async (page) => { await page.getByRole("button", { name: /^Incident/ }).click(); } },
    { name: "onboarding", path: "/", prepare: async (page) => { await page.evaluate(() => localStorage.setItem("subspace.onboarding", "active")); await page.reload(); } },
    { name: "palette", path: "/", overlay: true, prepare: async (page) => { await page.keyboard.press("Control+k"); } },
    ...settingsTabs.map((tab): Screen => ({
      name: `settings-${tab.toLowerCase().replaceAll(" ", "-")}`,
      path: "/settings",
      prepare: async (page) => { await page.getByRole("group", { name: "Settings" }).getByRole("button", { name: tab, exact: true }).click(); },
    })),
    { name: "decisions", path: "/briefs", overlay: true, prepare: inbox },
  ];
}

async function settle(page: Page) {
  await page.waitForLoadState("networkidle").catch(() => undefined);
  await page.waitForTimeout(600);
}

async function accessibility(page: Page, label: string, defects: string[]) {
  const { violations } = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "best-practice"]).analyze();
  for (const violation of violations) {
    if (violation.impact !== "serious" && violation.impact !== "critical") continue;
    defects.push(`${label}: ${violation.id} ${violation.nodes.slice(0, 3).map((node) => node.target.join(" ")).join(" | ")}`);
  }
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
          if (!screen.overlay) await accessibility(page, `${screen.name} ${viewport.name} ${theme}`, defects);
          const file = path.join(out, `${screen.name}-${viewport.name}-${theme}.png`);
          await page.screenshot({ path: file, fullPage: !screen.overlay });
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
