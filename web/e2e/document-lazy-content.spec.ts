import { test, expect } from "./support/fixtures";
import { createKnowledgeBase, deleteKnowledgeBase, waitForDocument } from "./support/api";

const filename = "lazy-content.md";
const blocks = Array.from({ length: 120 }, (_, index) => `BLOCK-${String(index).padStart(3, "0")} 这是用于验证大文档懒加载的段落。`).join("\n\n");

test("document content loads on demand and renders in batches", async ({ page, api }) => {
  test.setTimeout(180_000);
  const knowledgeBase = await createKnowledgeBase(api, `e2e-lazy-${Date.now()}`);
  const contentRequests: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/documents/") && request.url().includes("/content")) {
      contentRequests.push(request.url());
    }
  });
  try {
    const upload = await api.post(`knowledge-bases/${knowledgeBase.id}/documents`, {
      multipart: {
        file: { name: filename, mimeType: "text/markdown", buffer: Buffer.from(blocks, "utf8") },
      },
    });
    expect(upload.status()).toBe(202);
    await waitForDocument(api, knowledgeBase.id, filename, 120_000);

    await page.goto(`/knowledge-bases/${knowledgeBase.id}`);
    await page.getByRole("button", { name: /刷\s*新/ }).click();
    const row = page.getByTestId("document-name").filter({ hasText: filename }).locator("xpath=ancestor::tr");
    await expect(row).toBeVisible({ timeout: 20_000 });
    await row.getByRole("button", { name: /详\s*情/ }).click();

    const contentCard = page.locator(".ant-card").filter({ hasText: "已提取内容" });
    await expect(contentCard.getByRole("button", { name: "加载内容预览" })).toBeVisible({ timeout: 20_000 });
    expect(contentRequests).toHaveLength(0);

    await contentCard.getByRole("button", { name: "加载内容预览" }).click();
    await expect(contentCard.getByText("BLOCK-000", { exact: false })).toBeVisible({ timeout: 20_000 });
    await expect(contentCard.getByText("BLOCK-050", { exact: false })).toHaveCount(0);
    await expect(contentRequests.length).toBeGreaterThan(0);

    await contentCard.getByRole("button", { name: /加载更多块/ }).click();
    await expect(contentCard.getByText("BLOCK-050", { exact: false })).toBeVisible({ timeout: 20_000 });
  } finally {
    await deleteKnowledgeBase(api, knowledgeBase.id);
  }
});
