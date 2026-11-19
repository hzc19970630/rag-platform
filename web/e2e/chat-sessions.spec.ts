import { test, expect } from "./support/fixtures";
import { createKnowledgeBase, deleteKnowledgeBase, latestSessionId } from "./support/api";

test("knowledge base chat panel starts a scoped conversation inline", async ({ page, api }) => {
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

    await panel.getByTestId("kb-chat-history").hover();
    await expect(page.getByRole("tooltip").filter({ hasText: /历史会话|History/ })).toBeVisible();
    await panel.getByTestId("kb-chat-new").hover();
    await expect(page.getByRole("tooltip").filter({ hasText: /新会话|New conversation/ })).toBeVisible();

    await panel.getByTestId("kb-chat-new").click();
    await expect(panel.getByTestId("chat-composer")).toBeVisible();
    await expect(page).toHaveURL(new RegExp(`/knowledge-bases/${knowledgeBase.id}$`));

    await panel.getByTestId("kb-chat-history").click();
    await expect(page.getByTestId("kb-chat-history-list")).toContainText(/未命名会话|Untitled conversation/);
    await page.keyboard.press("Escape");

    const conversationId = await latestSessionId(api, knowledgeBase.id);
    expect(conversationId).not.toBe("");
    await page.goto(`/chat/${conversationId}`);
    const sessionList = page.getByTestId("chat-session-list");
    await expect(sessionList).toBeVisible();
    await expect(sessionList).toContainText(knowledgeBase.name);
    await expect(sessionList).toContainText(/未命名会话|Untitled conversation/);

    const groupToggle = sessionList.getByRole("button", { name: new RegExp(knowledgeBase.name) });
    await expect(groupToggle).toHaveAttribute("aria-expanded", "true");
    await groupToggle.click();
    await expect(groupToggle).toHaveAttribute("aria-expanded", "false");
    await groupToggle.click();
    await expect(groupToggle).toHaveAttribute("aria-expanded", "true");
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
    await expect(drawer.getByTestId("kb-chat-history")).toBeVisible();
    await expect(drawer.getByText(/还没有会话|No conversations yet/)).toBeVisible();
  } finally {
    if (knowledgeBaseId) await deleteKnowledgeBase(api, knowledgeBaseId);
  }
});