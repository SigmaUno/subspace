import { randomBytes } from "node:crypto";
import os from "node:os";
import path from "node:path";
import { defineConfig, devices } from "@playwright/test";

const apiPort = 18480;
const webPort = 13480;
const work = path.join(os.tmpdir(), "subspace-mobile-e2e");
process.env.MOBILE_E2E_API_URL = `http://127.0.0.1:${apiPort}`;
process.env.MOBILE_E2E_WEB_URL = `http://127.0.0.1:${webPort}`;
process.env.MOBILE_E2E_TOKEN ??= randomBytes(24).toString("hex");

const binary = process.env.SUBSPACE_E2E_SERVER_BIN || `${work}/subspace`;
const server = process.env.SUBSPACE_E2E_SERVER_BIN ? "true" : `CGO_ENABLED=0 go build -o ${binary} ../cmd/subspace`;

const clean = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith("SUBSPACE_")));

export default defineConfig({
  testDir: "e2e-mobile",
  outputDir: path.join(work, "results"),
  fullyParallel: false,
  workers: 1,
  timeout: 45_000,
  expect: { timeout: 10_000 },
  reporter: "list",
  use: { baseURL: process.env.MOBILE_E2E_WEB_URL, trace: "retain-on-failure" },
  webServer: [
    {
      command: `rm -rf ${work}/data && mkdir -p ${work}/data && ${server} && exec ${binary}`,
      url: `http://127.0.0.1:${apiPort}/healthz`,
      timeout: 180_000,
      env: {
        ...clean,
        SUBSPACE_ADDR: `127.0.0.1:${apiPort}`,
        SUBSPACE_DB: `${work}/data/subspace.db`,
        SUBSPACE_DATA_DIR: `${work}/data`,
        SUBSPACE_OWNER_TOKEN: process.env.MOBILE_E2E_TOKEN,
        SUBSPACE_PUBLIC_URL: `http://127.0.0.1:${apiPort}`,
        SUBSPACE_BACKUP_INTERVAL: "0",
      },
    },
    {
      command: `node_modules/.bin/next start --hostname 127.0.0.1 --port ${webPort}`,
      url: `http://127.0.0.1:${webPort}/login`,
      timeout: 60_000,
      env: { ...clean, NEXT_TELEMETRY_DISABLED: "1", SUBSPACE_API_URL: `http://127.0.0.1:${apiPort}` },
    },
  ],
  projects: [
    { name: "mobile", use: { ...devices["Pixel 7"], viewport: { width: 390, height: 844 } } },
    { name: "desktop", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 800 } } },
  ],
});
