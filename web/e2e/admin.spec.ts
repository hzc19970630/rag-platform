import { test, expect } from "./support/fixtures";

test("tenant administration loads overview, quota, members and audit", async ({ page }) => {
  await page.goto("/admin");

  await expect(page.getByRole("heading", { name: "组织管理", level: 2 })).toBeVisible();
  await expect(page.getByRole("tab", { name: "概览与配额" })).toBeVisible();
  await expect(page.getByText("组织信息")).toBeVisible();
  await expect(page.getByText("文档上限")).toBeVisible();
  await expect(page.getByRole("button", { name: /保\s*存/ })).toBeVisible();

  await page.getByRole("tab", { name: "成员" }).click();
  await expect(page.getByRole("tabpanel")).toContainText("dev-user");

  await page.getByRole("tab", { name: "审计" }).click();
  await expect(page.getByText("审计记录", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /导出 CSV/ })).toBeVisible();

  await page.locator(".admin-filter-field").filter({ hasText: "动作" }).locator(".ant-select").click();
  await page.keyboard.type("删除模型供应商");
  await expect(page.getByTitle("删除模型供应商")).toBeVisible();
  await page.keyboard.press("Escape");
});
