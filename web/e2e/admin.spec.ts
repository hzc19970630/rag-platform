import { test, expect } from "./support/fixtures";

test("tenant administration loads overview, quota, members and audit", async ({ page }) => {
  await page.goto("/admin");

  await expect(page.getByRole("heading", { name: "租户管理", level: 2 })).toBeVisible();
  await expect(page.getByRole("tab", { name: "概览与配额" })).toBeVisible();
  await expect(page.getByText("租户信息")).toBeVisible();
  await expect(page.getByText("文档上限")).toBeVisible();
  await expect(page.getByRole("button", { name: /保\s*存/ })).toBeVisible();

  await page.getByRole("tab", { name: "成员" }).click();
  await expect(page.getByRole("tabpanel")).toContainText("dev-user");

  await page.getByRole("tab", { name: "审计" }).click();
  await expect(page.getByText("审计记录", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /导出 CSV/ })).toBeVisible();
});
