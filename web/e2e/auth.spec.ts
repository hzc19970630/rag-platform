import { test, expect } from "./support/fixtures";

test("development login enters the workspace", async ({ page }) => {
  await page.route("**/api/v1/me", (route) =>
    route.fulfill({
      status: 401,
      contentType: "application/json",
      body: '{"error":"unauthenticated"}',
    }),
  );
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: "登录", level: 2 })).toBeVisible();
  await expect(page.getByTestId("login-dev-continue")).toBeVisible();

  await page.unroute("**/api/v1/me");
  await page.getByTestId("login-dev-continue").click();

  await expect(page).toHaveURL(/\/knowledge-bases$/);
  await expect(page.getByRole("heading", { name: "知识库", level: 2 })).toBeVisible();
  await expect(page.getByText("Development Knowledge Base").first()).toBeVisible();
});
