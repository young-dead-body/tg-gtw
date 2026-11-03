import { expect, test } from "@playwright/test";

const user = { id: "1", email: "employee@example.com", first_name: "Иван", last_name: "Иванов", role: "user" };

test("consent, login and Telegram navigation", async ({ page }) => {
  test.skip(!!process.env.E2E_BASE_URL, "Uses isolated API fixtures");
  let accepted = false;
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const data = path === "/api/me" ? { error: "unauthorized" }
      : path === "/api/features" ? { telegram_bot: false }
        : path === "/api/login" ? { user, consent_required: false }
          : { document_version: "1.0", required: true, accepted_at: new Date().toISOString() };
    if (path === "/api/consent" && route.request().method() === "POST") accepted = true;
    await route.fulfill({ status: path === "/api/me" ? 401 : 200, json: data });
  });
  await page.route("**/tg/", (route) => route.fulfill({ contentType: "text/html", body: "<h1>Рабочее пространство</h1>" }));
  await page.goto("/");
  await expect(page.getByRole("button", { name: "Войти", exact: true })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Текст согласия" })).toBeVisible();
  await page.getByRole("button", { name: "Принять", exact: true }).click();
  expect(accepted).toBe(true);
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await page.getByLabel("Email", { exact: true }).fill(user.email);
  await page.getByLabel("Пароль", { exact: true }).fill("valid-password");
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await expect(page).toHaveURL(/\/tg\/$/);
});

test("reset token leaves URL and is redeemed by POST only", async ({ page }) => {
  test.skip(!!process.env.E2E_BASE_URL, "Uses isolated API fixtures");
  const token = "a".repeat(64);
  let redeemed = false;
  await page.route("**/api/**", async (route) => {
    if (new URL(route.request().url()).pathname === "/api/password/reset") {
      const body = route.request().postDataJSON() as { token: string; password: string };
      expect(body.token).toBe(token);
      expect(body.password).toBe("new-password");
      redeemed = true;
    }
    await route.fulfill({ json: { document_version: "1.0", required: true } });
  });
  await page.goto(`/#reset=${token}`);
  await expect(page).toHaveURL(/\/$/);
  expect(redeemed).toBe(false);
  await page.getByLabel("Новый пароль", { exact: true }).fill("new-password");
  await page.getByLabel("Повторите пароль", { exact: true }).fill("new-password");
  await page.getByRole("button", { name: "Установить пароль", exact: true }).click();
  await expect(page.getByText(/Пароль изменён/)).toBeVisible();
  expect(redeemed).toBe(true);
});

test("live Telegram QR uses authenticated same-origin DC", async ({ page }) => {
  test.skip(!process.env.E2E_BASE_URL || !process.env.E2E_EMAIL || !process.env.E2E_PASSWORD, "Requires a dedicated stage account");
  const dcSockets: string[] = [];
  page.on("websocket", (socket) => dcSockets.push(socket.url()));
  await page.goto("/");
  const accept = page.getByRole("button", { name: "Принять", exact: true });
  if (await accept.isVisible()) await accept.click();
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await page.getByLabel("Email", { exact: true }).fill(process.env.E2E_EMAIL!);
  await page.getByLabel("Пароль", { exact: true }).fill(process.env.E2E_PASSWORD!);
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await expect(page).toHaveURL(/\/tg\//);
  await expect(page.locator(".qr-container canvas, canvas.qr-canvas, .qr-code canvas, .qr-container svg").first()).toBeVisible({ timeout: 30_000 });
  expect(dcSockets.some((url) => url.includes("/tg/_gw/dc/"))).toBe(true);
  expect(dcSockets.every((url) => new URL(url).host === new URL(process.env.E2E_BASE_URL!).host)).toBe(true);
});
