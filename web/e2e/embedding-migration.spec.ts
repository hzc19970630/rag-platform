import { resolve } from "node:path";
import { test, expect } from "./support/fixtures";
import { createKnowledgeBase, deleteKnowledgeBase, waitForDocument } from "./support/api";

const screenshotDir = "C:/rag-ui-audit";

async function expectNoPageOverflow(page: import("@playwright/test").Page, label: string) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow, `${label}: document overflow ${overflow}px`).toBeLessThanOrEqual(1);
}

test("embedding migration panel renders, targets are selectable and layout stays inside the viewport", async ({ page, api }) => {
  test.setTimeout(240_000);
  const suffix = Date.now();
  const knowledgeBaseName = `e2e-migration-${suffix}`;
  const modelA = `e2e-embed-a-${suffix}`;
  const modelB = `e2e-embed-b-${suffix}`;
  let knowledgeBaseId = "";
  let emptyKnowledgeBaseId = "";
  let providerId = "";

  try {
    const provider = await api.post("models/providers", {
      data: {
        name: `e2e-embed-${suffix}`,
        kind: "openai_compatible",
        base_url: "http://host.docker.internal:9/v1",
        models: [
          { model: modelA, capability: "embedding", dimension: 8 },
          { model: modelB, capability: "embedding", dimension: 16 },
        ],
      },
    });
    expect(provider.status()).toBe(201);
    providerId = ((await provider.json()) as { id: string }).id;

    const knowledgeBase = await createKnowledgeBase(api, knowledgeBaseName);
    knowledgeBaseId = knowledgeBase.id;
    await page.goto(`/knowledge-bases/${knowledgeBaseId}`);
    await page.getByTestId("document-upload-input").setInputFiles(
      resolve(process.cwd(), "../examples/demo/retrieval-handbook.md"),
    );
    await waitForDocument(api, knowledgeBaseId, "retrieval-handbook.md");

    await page.goto(`/knowledge-bases/${knowledgeBaseId}?settings=1`);
    await page.getByRole("menuitem", { name: "模型路由" }).click();
    await expect(page.getByTestId("embedding-migration")).toBeVisible();
    await page.getByTestId("embedding-migration").scrollIntoViewIfNeeded();
    await expect(page.getByText("模型迁移", { exact: true })).toBeVisible();
    await expect(page.getByText("当前空间")).toBeVisible();
    await expect(page.getByText(/迁移期间该知识库暂停入库/)).toBeVisible();

    const target = page.getByTestId("embedding-migration-target");
    await target.click();
    await page.locator(".ant-select-item-option").filter({ hasText: modelB }).click();
    const start = page.getByTestId("embedding-migration-start");
    await expect(start).toBeEnabled();

    await page.setViewportSize({ width: 1280, height: 900 });
    await expectNoPageOverflow(page, "1280");
    await page.screenshot({ path: `${screenshotDir}/d3-migration-1280.png` });

    await page.setViewportSize({ width: 375, height: 812 });
    await page.getByTestId("embedding-migration").scrollIntoViewIfNeeded();
    await expect(page.getByTestId("embedding-migration")).toBeVisible();
    await expectNoPageOverflow(page, "375");
    const drawerOverflow = await page.evaluate(() => {
      const body = document.querySelector(".ant-drawer-body") as HTMLElement | null;
      return body ? body.scrollWidth - body.clientWidth : 0;
    });
    expect(drawerOverflow, "settings drawer horizontal overflow").toBeLessThanOrEqual(1);
    await page.screenshot({ path: `${screenshotDir}/d3-migration-375.png` });

    // An empty knowledge base edits the binding directly instead of migrating.
    const empty = await createKnowledgeBase(api, `e2e-empty-${suffix}`);
    emptyKnowledgeBaseId = empty.id;
    await page.goto(`/knowledge-bases/${emptyKnowledgeBaseId}?settings=1`);
    await page.getByRole("menuitem", { name: "模型路由" }).click();
    await expect(page.getByTestId("embedding-migration")).toBeVisible();
    await expect(page.getByTestId("embedding-direct-save")).toBeVisible();
    await expect(page.getByTestId("embedding-migration-target")).toHaveCount(0);
  } finally {
    if (emptyKnowledgeBaseId) {
      await deleteKnowledgeBase(api, emptyKnowledgeBaseId).catch(() => undefined);
    }
    if (knowledgeBaseId) {
      await deleteKnowledgeBase(api, knowledgeBaseId).catch(() => undefined);
    }
    if (providerId) {
      await api.delete(`models/providers/${providerId}`).catch(() => undefined);
    }
  }
});
