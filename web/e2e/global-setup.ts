import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { randomBytes } from "node:crypto";
import { existsSync } from "node:fs";
import { cp, mkdtemp, rm } from "node:fs/promises";
import { createServer } from "node:net";
import os from "node:os";
import path from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const web = path.resolve(__dirname, "..");
const root = path.resolve(web, "..");

function freePort() {
  return new Promise<number>((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      server.close(() => typeof address === "object" && address ? resolve(address.port) : reject(new Error("no port")));
    });
  });
}

function start(name: string, command: string, args: string[], extra: Record<string, string>, cwd: string) {
  const env: NodeJS.ProcessEnv = { ...process.env };
  for (const key of Object.keys(env)) if (key.startsWith("SUBSPACE_")) delete env[key];
  const child = spawn(command, args, { cwd, env: { ...env, ...extra }, stdio: ["ignore", "pipe", "pipe"] });
  const output: string[] = [];
  const collect = (chunk: Buffer) => { output.push(chunk.toString()); if (output.length > 200) output.shift(); };
  child.stdout?.on("data", collect);
  child.stderr?.on("data", collect);
  return { name, child, output };
}

async function ready(url: string, process: { name: string; child: ChildProcess; output: string[] }) {
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    if (process.child.exitCode !== null) break;
    const status = await fetch(url, { redirect: "manual" }).then((response) => response.status, () => 0);
    if (status === 200) return;
    await delay(100);
  }
  throw new Error(`${process.name} did not become ready at ${url}\n${process.output.join("")}`);
}

export default async function globalSetup() {
  const standalone = path.join(web, ".next", "standalone", "server.js");
  if (!existsSync(standalone)) throw new Error("Run `pnpm build` before `pnpm test:e2e`.");
  await cp(path.join(web, ".next", "static"), path.join(web, ".next", "standalone", ".next", "static"), { recursive: true, force: true });

  const temp = await mkdtemp(path.join(os.tmpdir(), "subspace-e2e-"));
  const binary = process.env.SUBSPACE_E2E_SERVER_BIN || path.join(temp, "subspace");
  if (!process.env.SUBSPACE_E2E_SERVER_BIN) {
    execFileSync("go", ["build", "-o", binary, "./cmd/subspace"], { cwd: root, stdio: "inherit" });
  }

  const helper = path.join(temp, "subspace-e2e");
  execFileSync("go", ["build", "-o", helper, "./cmd/subspace-e2e"], { cwd: root, stdio: "inherit" });

  const token = randomBytes(24).toString("hex");
  const [apiPort, webPort] = [await freePort(), await freePort()];
  const api = `http://127.0.0.1:${apiPort}`;
  const site = `http://127.0.0.1:${webPort}`;

  const server = start("subspace", binary, [], {
    SUBSPACE_ADDR: `127.0.0.1:${apiPort}`,
    SUBSPACE_DB: path.join(temp, "subspace.db"),
    SUBSPACE_DATA_DIR: temp,
    SUBSPACE_OWNER_TOKEN: token,
    SUBSPACE_PUBLIC_URL: api,
    SUBSPACE_BACKUP_INTERVAL: "0",
    SUBSPACE_SPAWNER: "fake",
  }, temp);
  const next = start("next", process.execPath, [standalone], {
    NODE_ENV: "production",
    NEXT_TELEMETRY_DISABLED: "1",
    HOSTNAME: "127.0.0.1",
    PORT: String(webPort),
    SUBSPACE_API_URL: api,
  }, path.dirname(standalone));

  const stop = async () => {
    for (const { child } of [next, server]) {
      if (child.exitCode !== null) continue;
      const exited = new Promise((resolve) => child.once("exit", resolve));
      child.kill("SIGTERM");
      await exited;
    }
    await rm(temp, { recursive: true, force: true });
  };

  try {
    await ready(`${api}/healthz`, server);
    if (process.env.E2E_SEED_DEMO) {
      const seeder = path.join(temp, "subspace-seed");
      execFileSync("go", ["build", "-o", seeder, "./cmd/subspace-seed"], { cwd: root, stdio: "inherit" });
      execFileSync(seeder, ["-url", api, "-seed", "1"], { cwd: temp, stdio: "inherit", env: { ...process.env, SUBSPACE_OWNER_TOKEN: token } });
    }
    await ready(`${site}/login`, next);
  } catch (error) {
    await stop();
    throw error;
  }

  process.env.E2E_API_URL = api;
  process.env.E2E_WEB_URL = site;
  process.env.E2E_OWNER_TOKEN = token;
  process.env.E2E_HELPER_BIN = helper;
  return stop;
}
