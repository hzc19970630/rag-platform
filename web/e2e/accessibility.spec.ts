import AxeBuilder from "@axe-core/playwright";
import type { Page } from "@playwright/test";
import { test, expect } from "./support/fixtures";

const wcagTags = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"];

async function expectNoViolations(page: Page): Promise<void> {
  const results = await new AxeBuilder({ page }).withTags(wcagTags).analyze();
  const violations = results.violations.map((violation) => {
    const targets = violation.nodes.map((node) => node.target.join(" ")).join(" | ");
    return `${violation.id} (${violation.impact}): ${violation.help} [${targets}]`;
  });
  expect(violations).toEqual([]);
}

test("login page has no WCAG 2.1 AA violations", async ({ page }) => {
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
  await expectNoViolations(page);
});

test("knowledge base list has no WCAG 2.1 AA violations", async ({ page }) => {
  await page.goto("/knowledge-bases");
  await expect(page.getByRole("heading", { name: "知识库", level: 2 })).toBeVisible();
  await expectNoViolations(page);
});

test("tenant administration has no WCAG 2.1 AA violations", async ({ page }) => {
  await page.goto("/admin");
  await expect(page.getByRole("heading", { name: "租户管理", level: 2 })).toBeVisible();
  await expect(page.getByText("文档上限")).toBeVisible();
  await expectNoViolations(page);
});

test("global search is keyboard accessible and Escape closes it", async ({ page }) => {
  await page.goto("/knowledge-bases");
  // Wait for the shell to mount: the shortcut listener is attached in an effect after first paint.
  await expect(page.getByRole("button", { name: /搜索知识库/ })).toBeVisible();
  await page.keyboard.press("Control+k");

  const dialog = page.getByRole("dialog").filter({ hasText: "搜索知识库" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("textbox", { name: "搜索知识库" })).toBeFocused();

  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
});
