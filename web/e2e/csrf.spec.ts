import { expect, owner, signIn, test, uniqueID } from "./subspace";

test("session-authenticated writes through the proxy need the CSRF header", async ({ page }) => {
  await signIn(page);
  const title = uniqueID("Brief");
  const brief = { title, body: "csrf", repos: [], max_cost_eur: 1, max_parallel_agents: 1, allowed_providers: [] };
  const titles = async () => (await owner<{ briefs: { title: string }[] }>("briefs")).briefs.map((item) => item.title);
  const charter = async () => (await owner<{ text: string }>("charter")).text;
  const before = await charter();

  const writes: [string, string, unknown][] = [
    ["POST", "briefs", brief],
    ["POST", "pause-all", { reason: "csrf" }],
    ["PUT", "charter", { text: "forged" }],
    ["PATCH", "charter", { text: "forged" }],
    ["DELETE", "artifacts/A-1", undefined],
  ];
  for (const [method, path, data] of writes) {
    const bare = await page.request.fetch(`/api/v1/${path}`, { method, data });
    expect(bare.status(), `${method} ${path}`).toBe(403);
    const flagged = await page.request.fetch(`/api/v1/${path}`, { method, data, headers: { "X-Subspace-Request": "0" } });
    expect(flagged.status(), `${method} ${path} flag 0`).toBe(403);
    const crossSite = await page.request.fetch(`/api/v1/${path}`, {
      method, data, headers: { "X-Subspace-Request": "1", "Sec-Fetch-Site": "cross-site" },
    });
    expect(crossSite.status(), `${method} ${path} cross-site`).toBe(403);
  }
  expect((await owner<{ paused: boolean }>("state")).paused).toBe(false);
  expect(await charter()).toBe(before);
  expect(await titles()).not.toContain(title);

  await page.goto("/");
  const fromPage = await page.evaluate(async () => {
    const response = await fetch("/api/v1/pause-all", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
    return response.status;
  });
  expect(fromPage).toBe(403);
  expect((await owner<{ paused: boolean }>("state")).paused).toBe(false);

  const accepted = await page.request.post("/api/v1/briefs", { data: brief, headers: { "X-Subspace-Request": "1" } });
  expect(accepted.status()).toBe(201);
  await expect.poll(titles).toContain(title);
});
