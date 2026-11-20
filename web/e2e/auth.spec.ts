import { expect, ownerToken, test } from "./subspace";

test("owner signs in with the owner token and a wrong token is rejected", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveURL(/\/login$/);

  await page.getByLabel("Owner token").fill(`wrong-${ownerToken()}`);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/login\?error=rejected$/);
  await expect(page.getByRole("alert").filter({ hasText: "Owner token" })).toHaveText("Owner token rejected.");
  expect(await page.context().cookies()).toEqual([]);

  await page.getByLabel("Owner token").fill(ownerToken());
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole("heading", { name: "Swarm overview" })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "Live" })).toBeVisible();

  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/login$/);
  await page.goto("/agents");
  await expect(page).toHaveURL(/\/login$/);
});

test("API proxy routes refuse anonymous and forged access", async ({ request, playwright, baseURL }) => {
  for (const path of ["state", "briefs", "agents", "decisions", "audit", "artifacts?scope=agent:x"]) {
    const response = await request.get(`/api/v1/${path}`);
    expect(response.status(), path).toBe(401);
    expect(response.headers()["www-authenticate"]).toBe("Bearer");
  }
  for (const path of ["pause-all", "resume-all", "briefs", "agents", "threads/agent/x/messages"]) {
    const response = await request.post(`/api/v1/${path}`, { data: {} });
    expect(response.status(), path).toBe(401);
  }
  expect((await request.patch("/api/v1/charter", { data: { text: "x" } })).status()).toBe(401);
  expect((await request.delete("/api/v1/artifacts/A-1")).status()).toBe(401);

  const events = await request.get("/api/v1/events", { headers: { Accept: "text/event-stream" } });
  expect(events.status()).toBe(401);

  const forged = await request.get("/api/v1/state", { headers: { Authorization: `Bearer forged-${ownerToken()}` } });
  expect(forged.status()).toBe(401);
  const forgedCookie = await request.get("/api/v1/briefs", { headers: { Cookie: `subspace_owner=forged-${ownerToken()}` } });
  expect(forgedCookie.status()).toBe(401);

  const session = await playwright.request.newContext({
    baseURL,
    storageState: { cookies: [{ name: "subspace_owner", value: ownerToken(), domain: new URL(baseURL!).hostname, path: "/", expires: -1, httpOnly: true, secure: false, sameSite: "Strict" }], origins: [] },
  });
  expect((await session.get("/api/v1/state")).status()).toBe(200);
  expect((await session.post("/api/v1/pause-all", { data: {} })).status()).toBe(403);
  expect((await session.post("/api/v1/pause-all", { data: {}, headers: { "X-Subspace-Request": "1", "Sec-Fetch-Site": "cross-site" } })).status()).toBe(403);
  await session.dispose();
});
