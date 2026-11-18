import { test, expect } from "./support/fixtures";
import { createKnowledgeBase, deleteKnowledgeBase } from "./support/api";

test("knowledge base chat panel creates and opens a scoped session", async ({ page, api }) => {
  test.setTimeout(120_000);
  const suffix = Date.now();
  let knowledgeBaseId = "";

  try {
    const knowledgeBase = await createKnowledgeBase(api, `e2e-chat-panel-${suffix}`);
    knowledgeBaseId = knowledgeBase.id;

    await page.goto(`/knowledge-bases/${knowledgeBase.id}`);
    await page.getByTestId("kb-chat-toggle").click();

    const panel = page.getByTestId("kb-chat-panel");
    await expect(panel).toBeVisible();
    await expect(panel.getByText(/还没有会话|No conversations yet/)).toBeVisible();

    await panel.getByTestId("session-new").click();
    await expect(page).toHaveURL(/\/chat\/[^/?#]+$/);

    // The conversation view no longer carries a session list.
    await expect(page.getByTestId("chat-session-list")).toHaveCount(0);

    await page.getByRole("button", { name: "返回" }).click();
    await expect(page).toHaveURL(new RegExp(`/knowledge-bases/${knowledgeBase.id}$`));
  } finally {
    if (knowledgeBaseId) await deleteKnowledgeBase(api, knowledgeBaseId);
  }
});

test("chat panel collapses into a drawer on narrow screens", async ({ page, api }) => {
  test.setTimeout(120_000);
  const suffix = Date.now();
  let knowledgeBaseId = "";

  try {
    const knowledgeBase = await createKnowledgeBase(api, `e2e-chat-drawer-${suffix}`);
    knowledgeBaseId = knowledgeBase.id;

    await page.setViewportSize({ width: 900, height: 800 });
    await page.goto(`/knowledge-bases/${knowledgeBase.id}`);
    await page.getByTestId("kb-chat-toggle").click();

    await expect(page.getByTestId("kb-chat-panel")).toHaveCount(0);
    const drawer = page.locator(".ant-drawer-body");
    await expect(drawer).toBeVisible();
    await expect(drawer.getByTestId("session-new")).toBeVisible();
  } finally {
    if (knowledgeBaseId) await deleteKnowledgeBase(api, knowledgeBaseId);
  }
});
