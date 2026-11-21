import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";

const web = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const root = resolve(web, "..");
const agents = Number(process.env.PERF_AGENTS ?? 1000);
const tasksPerAgent = Number(process.env.PERF_TASKS_PER_AGENT ?? 20);
const settle = Number(process.env.PERF_LIVE_SECONDS ?? 10);
const enforce = process.argv.includes("--enforce");
const out = process.env.PERF_OUT;
const token = "perf-owner-token-0123456789abcdef";
const limits = { interaction: 100, longTask: 200 };

const dir = mkdtempSync(join(tmpdir(), "subspace-perf-"));
const children = [];
let container = "";

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { stdio: "inherit", ...options });
  if (result.status !== 0) throw new Error(`${command} ${args.join(" ")} failed`);
}

function start(command, args, env, cwd, quiet = false) {
  const child = spawn(command, args, { cwd, env: { ...process.env, ...env }, stdio: ["ignore", "ignore", quiet ? "ignore" : "inherit"] });
  children.push(child);
  return child;
}

function freePort() {
  return new Promise((resolvePort, reject) => {
    const probe = createServer();
    probe.once("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const { port } = probe.address();
      probe.close(() => resolvePort(port));
    });
  });
}

const sleep = (ms) => new Promise((done) => setTimeout(done, ms));

async function until(label, check, timeout = 120_000) {
  const deadline = Date.now() + timeout;
  for (;;) {
    try {
      const value = await check();
      if (value) return value;
    } catch {}
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${label}`);
    await sleep(500);
  }
}

async function owner(base, path) {
  const response = await fetch(`${base}/api/v1/${path}`, { headers: { Authorization: `Bearer ${token}` } });
  if (!response.ok) throw new Error(`${path}: HTTP ${response.status}`);
  return response.json();
}

const probe = () => {
  const store = { tasks: [], events: [] };
  window.__perf = store;
  new PerformanceObserver((list) => {
    for (const entry of list.getEntries()) store.tasks.push({ at: entry.startTime, duration: entry.duration });
  }).observe({ type: "longtask", buffered: true });
  new PerformanceObserver((list) => {
    for (const entry of list.getEntries()) store.events.push({ at: entry.startTime, name: entry.name, duration: entry.duration });
  }).observe({ type: "event", durationThreshold: 16, buffered: true });
};

async function measure(page, name, action) {
  const from = await page.evaluate(() => performance.now());
  const started = Date.now();
  await action();
  await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))));
  const wall = Date.now() - started;
  await sleep(300);
  const sample = await page.evaluate((since) => ({
    tasks: window.__perf.tasks.filter((entry) => entry.at >= since),
    events: window.__perf.events.filter((entry) => entry.at >= since),
  }), from);
  const latency = sample.events.reduce((max, entry) => Math.max(max, entry.duration), 0);
  const longest = sample.tasks.reduce((max, entry) => Math.max(max, entry.duration), 0);
  return { name, wall, latency, longTasks: sample.tasks.length, longest };
}

function table(rows) {
  const columns = [
    ["step", (row) => row.name],
    ["wall ms", (row) => Math.round(row.wall)],
    ["input latency ms", (row) => Math.round(row.latency)],
    ["long tasks", (row) => row.longTasks],
    ["longest task ms", (row) => Math.round(row.longest)],
    ["verdict", (row) => row.pass ? "ok" : "FAIL"],
  ];
  const cells = [columns.map(([title]) => title), ...rows.map((row) => columns.map(([, cell]) => String(cell(row))))];
  const widths = columns.map((_, index) => Math.max(...cells.map((line) => line[index].length)));
  const format = (line) => line.map((cell, index) => index ? cell.padStart(widths[index]) : cell.padEnd(widths[index])).join("  ");
  console.log(format(cells[0]));
  console.log(widths.map((width) => "-".repeat(width)).join("  "));
  cells.slice(1).forEach((line) => console.log(format(line)));
}

async function launch() {
  if (!process.env.PERF_DOCKER) return chromium.launch({ executablePath: process.env.PERF_CHROMIUM || undefined });
  const port = await freePort();
  container = `subspace-perf-${port}`;
  const image = process.env.PERF_IMAGE || "mcr.microsoft.com/playwright:v1.65.1-noble";
  start("docker", [
    "run", "--rm", "--name", container, "--network", "host", "--ipc", "host", "-u", `${process.getuid()}:${process.getgid()}`, "-e", "HOME=/tmp",
    "-v", `${web}:${web}:ro`, "-w", web, image,
    "node", "node_modules/playwright-core/cli.js", "run-server", "--host", "127.0.0.1", "--port", String(port), "--path", "/perf",
  ], {}, web);
  const endpoint = `ws://127.0.0.1:${port}/perf`;
  return until("browser server", () => chromium.connect(endpoint), 60_000);
}

async function main() {
  const [serverPort, webPort] = [await freePort(), await freePort()];
  const base = `http://127.0.0.1:${serverPort}`;
  const bin = (name) => join(dir, name);
  run("go", ["build", "-o", bin("subspace"), "./cmd/subspace"], { cwd: root, env: { ...process.env, CGO_ENABLED: "0" } });
  run("go", ["build", "-o", bin("loadtest"), "./cmd/loadtest"], { cwd: root, env: { ...process.env, CGO_ENABLED: "0" } });
  if (!existsSync(join(web, ".next", "BUILD_ID"))) run("pnpm", ["build"], { cwd: web });

  mkdirSync(join(dir, "data"));
  start(bin("subspace"), [], {
    SUBSPACE_ADDR: `127.0.0.1:${serverPort}`, SUBSPACE_DB: join(dir, "data", "subspace.db"),
    SUBSPACE_OWNER_TOKEN: token, SUBSPACE_PUBLIC_URL: base,
  }, root);
  await until("server", async () => (await fetch(`${base}/healthz`)).ok, 30_000);

  const duration = Math.ceil(settle + 90);
  start(bin("loadtest"), [
    "-url", base, "-owner-token", token, "-agents", String(agents), "-tasks-per-agent", String(tasksPerAgent),
    "-duration", `${duration}s`, "-ramp", "15s", "-sse-subs", "0", "-drain", "1s",
  ], {}, root, true);
  await until("seeded swarm", async () => {
    const state = await owner(base, "state");
    const tasks = Object.values(state.counts.tasks ?? {}).reduce((sum, count) => sum + count, 0);
    return state.counts.agents >= agents && tasks >= agents * tasksPerAgent * 0.95;
  });
  const inProgress = await until("claimed tasks", async () => {
    const count = (await owner(base, "state")).counts.tasks.in_progress ?? 0;
    return count >= agents / 4 && count;
  });
  console.log(`seeded ${agents} agents, ${tasksPerAgent * agents} tasks, ${inProgress} in progress`);

  start("node", [join(web, "node_modules", "next", "dist", "bin", "next"), "start", "-p", String(webPort), "-H", "127.0.0.1"],
    { SUBSPACE_API_URL: base }, web);
  const site = `http://127.0.0.1:${webPort}`;
  await until("web", async () => (await fetch(`${site}/login`)).ok, 60_000);

  const browser = await launch();
  const context = await browser.newContext({ viewport: { width: 1600, height: 1000 } });
  await context.addCookies([{ name: "subspace_owner", value: token, url: site }]);
  await context.addInitScript(probe);
  const page = await context.newPage();
  page.on("console", (message) => { if (message.type() === "error") console.error(`console: ${message.text()}`); });
  page.on("requestfailed", (request) => {
    if (request.failure()?.errorText !== "net::ERR_ABORTED") console.error(`request failed: ${request.url()} ${request.failure()?.errorText}`);
  });
  page.on("response", (response) => { if (response.status() >= 400) console.error(`http ${response.status()}: ${response.url()}`); });
  page.on("pageerror", (error) => console.error(`page error: ${error.message}`));
  const rows = [];

  rows.push(await measure(page, "load overview", async () => {
    await page.goto(site, { waitUntil: "commit" });
    await page.locator(".agent-row").first().waitFor({ timeout: 120_000 });
    await page.locator(".board .task").first().waitFor({ timeout: 120_000 });
  }));
  rows[0].ready = await page.evaluate(() => performance.now());

  rows.push(await measure(page, `live updates ${settle}s`, () => sleep(settle * 1000)));
  rows.push(await measure(page, "filter workload (type)", async () => {
    const input = page.getByLabel("Filter workload");
    await input.click();
    await page.keyboard.type("loadtest", { delay: 60 });
  }));
  rows.push(await measure(page, "clear workload filter", () => page.getByLabel("Filter workload").fill("")));
  rows.push(await measure(page, "agent scope: all", () => page.getByLabel("Agent scope").selectOption("all")));
  rows.push(await measure(page, "scroll agent list", async () => {
    const list = page.locator(".agent-list").first();
    await list.hover();
    for (let step = 0; step < 30; step++) {
      await page.mouse.wheel(0, 600);
      await sleep(30);
    }
  }));
  rows.push(await measure(page, "filter tasks (type)", async () => {
    await page.getByLabel("Filter tasks").click();
    await page.keyboard.type("task", { delay: 60 });
  }));
  rows.push(await measure(page, "clear task filter", () => page.getByLabel("Filter tasks").fill("")));
  rows.push(await measure(page, "task status filter", () => page.getByLabel("Task status").selectOption("in_progress")));
  rows.push(await measure(page, "all task statuses", () => page.getByLabel("Task status").selectOption("")));
  rows.push(await measure(page, "scroll board column", async () => {
    const column = page.locator('.column[data-status="assigned"] .column-list');
    await column.hover();
    for (let step = 0; step < 30; step++) {
      await page.mouse.wheel(0, 800);
      await sleep(30);
    }
  }));
  rows.push(await measure(page, "open agent thread", async () => {
    await page.locator(".agent-row .text-button").first().click();
    await page.locator("aside.thread .messages article, aside.thread .empty").first().waitFor({ timeout: 30_000 });
  }));
  rows.push(await measure(page, "thread live updates 5s", () => sleep(5000)));
  rows.push(await measure(page, "close thread", () => page.keyboard.press("Escape")));
  rows.push(await measure(page, "agents view", async () => {
    await page.getByRole("navigation", { name: "Workspace" }).getByText("Agents", { exact: true }).click();
    await page.locator(".registry-row").nth(1).waitFor({ timeout: 60_000 });
  }));
  rows.push(await measure(page, "scroll agents table", async () => {
    await page.locator(".registry-list").hover();
    for (let step = 0; step < 30; step++) {
      await page.mouse.wheel(0, 600);
      await sleep(30);
    }
  }));

  rows.forEach((row) => { row.pass = row.latency <= limits.interaction && row.longest <= limits.longTask; });
  await browser.close();

  console.log(`\nweb perf: ${agents} agents, ${tasksPerAgent * agents} tasks, ready ${Math.round(rows[0].ready)} ms (limits: input ${limits.interaction} ms, long task ${limits.longTask} ms)\n`);
  table(rows);
  if (out) writeFileSync(out, JSON.stringify({ agents, tasks: tasksPerAgent * agents, limits, rows }, null, 2));
  return rows.every((row) => row.pass);
}

let passed = false;
try {
  passed = await main();
} catch (error) {
  console.error(error);
  process.exitCode = 1;
} finally {
  children.forEach((child) => child.kill("SIGTERM"));
  if (container) spawnSync("docker", ["rm", "-f", container], { stdio: "ignore" });
  await sleep(500);
  rmSync(dir, { recursive: true, force: true });
}
if (enforce && !passed) process.exitCode = 1;
