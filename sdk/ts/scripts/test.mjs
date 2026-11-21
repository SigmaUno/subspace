import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtemp, readdir, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, "..", "..", "..");
const suite = resolve(here, "..", "test");

function freePort() {
  return new Promise((ok, fail) => {
    const probe = createServer();
    probe.once("error", fail);
    probe.listen(0, "127.0.0.1", () => {
      const { port } = probe.address();
      probe.close(() => ok(port));
    });
  });
}

async function healthy(url, server, deadline) {
  while (Date.now() < deadline) {
    if (server.exitCode !== null) throw new Error(`server exited with code ${server.exitCode}`);
    try {
      const res = await fetch(`${url}/healthz`);
      if (res.ok) return;
    } catch {}
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error("server did not become healthy");
}

function exited(child) {
  return new Promise((ok) => {
    if (child.exitCode !== null) return ok(child.exitCode);
    child.once("exit", (code) => ok(code ?? 1));
  });
}

async function stop(server) {
  if (server.exitCode !== null) return;
  try {
    process.kill(-server.pid, "SIGTERM");
  } catch {}
  const timer = setTimeout(() => {
    try {
      process.kill(-server.pid, "SIGKILL");
    } catch {}
  }, 10_000);
  await exited(server);
  clearTimeout(timer);
}

const dir = await mkdtemp(join(tmpdir(), "subspace-sdk-"));
const port = await freePort();
const url = `http://127.0.0.1:${port}`;
const token = randomBytes(24).toString("base64url");
const server = spawn("go", ["run", "./cmd/subspace"], {
  cwd: root,
  detached: true,
  stdio: ["ignore", "ignore", "inherit"],
  env: {
    ...process.env,
    SUBSPACE_ADDR: `127.0.0.1:${port}`,
    SUBSPACE_DB: join(dir, "subspace.db"),
    SUBSPACE_OWNER_TOKEN: token,
    SUBSPACE_PUBLIC_URL: url,
    SUBSPACE_LOG_LEVEL: "warn",
  },
});

let code = 1;
try {
  await healthy(url, server, Date.now() + 240_000);
  const files = (await readdir(suite)).filter((f) => f.endsWith(".test.ts")).sort().map((f) => join(suite, f));
  const run = spawn(
    process.execPath,
    ["--experimental-strip-types", "--disable-warning=ExperimentalWarning", "--test", "--test-concurrency=1", ...files],
    { stdio: "inherit", env: { ...process.env, SUBSPACE_TEST_URL: url, SUBSPACE_TEST_OWNER_TOKEN: token } },
  );
  code = await exited(run);
} catch (err) {
  console.error(err instanceof Error ? err.message : err);
} finally {
  await stop(server);
  await rm(dir, { recursive: true, force: true });
}
process.exit(code);
