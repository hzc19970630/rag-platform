import { test, expect } from "./support/fixtures";

const viewports = [
  { name: "mobile", width: 375, height: 812 },
  { name: "tablet", width: 768, height: 1024 },
  { name: "desktop", width: 1280, height: 800 },
];

async function expectNoHorizontalOverflow(page: import("@playwright/test").Page): Promise<void> {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);
}

for (const viewport of viewports) {
  test(`knowledge base list fits ${viewport.name} viewport`, async ({ page }) => {
    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    await page.goto("/knowledge-bases");
    await expect(page.getByRole("heading", { name: "知识库", level: 2 })).toBeVisible();
    await expectNoHorizontalOverflow(page);

    if (viewport.width < 768) {
      await expect(page.getByRole("button", { name: "open navigation" })).toBeVisible();
    }
  });
}

test("chat empty state fits mobile viewport", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.goto("/chat");
  await expect(page.getByText("选择知识库开始提问")).toBeVisible();
  await expectNoHorizontalOverflow(page);
});
