import os from "node:os";
import path from "node:path";
import { defineConfig, devices } from "@playwright/test";

process.env.E2E_SEED_DEMO = "1";

export default defineConfig({
  testDir: "e2e-a11y",
  globalSetup: "./e2e/global-setup.ts",
  outputDir: path.join(os.tmpdir(), "subspace-a11y-results"),
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? [["list"], ["github"]] : "list",
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 800 } } },
    { name: "mobile", use: { ...devices["Pixel 7"], viewport: { width: 390, height: 844 } } },
  ],
});
