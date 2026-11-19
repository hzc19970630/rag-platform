import { test, expect } from "./support/fixtures";
import { createKnowledgeBase, deleteKnowledgeBase } from "./support/api";

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

test("knowledge base workspace fills the desktop area", async ({ page, api }) => {
  const knowledgeBase = await createKnowledgeBase(api, `e2e-layout-${Date.now()}`);
  try {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(`/knowledge-bases/${knowledgeBase.id}`);
    await expect(page.getByRole("heading", { name: knowledgeBase.name, level: 2 })).toBeVisible();

    const folder = await page.locator(".folder-panel").boundingBox();
    const documents = await page.locator(".kb-documents-main").boundingBox();
    expect(folder).not.toBeNull();
    expect(documents).not.toBeNull();
    expect(Math.abs(folder!.height - documents!.height)).toBeLessThanOrEqual(1);

    await page.getByRole("tab", { name: "成员" }).click();
    const settingsTab = page.locator(".kb-settings-tab:visible");
    const section = settingsTab.locator(".settings-section");
    await expect(section).toBeVisible();
    const tabBox = await settingsTab.boundingBox();
    const sectionBox = await section.boundingBox();
    expect(tabBox).not.toBeNull();
    expect(sectionBox).not.toBeNull();
    expect(Math.abs(sectionBox!.width - tabBox!.width)).toBeLessThanOrEqual(1);
    expect(Math.abs(sectionBox!.height - tabBox!.height)).toBeLessThanOrEqual(1);
    expect(sectionBox!.width).toBeGreaterThan(900);
    await expectNoHorizontalOverflow(page);
  } finally {
    await deleteKnowledgeBase(api, knowledgeBase.id);
  }
});
