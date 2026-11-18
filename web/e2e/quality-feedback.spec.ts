import { resolve } from "node:path";
import { test, expect } from "./support/fixtures";
import { createKnowledgeBase, deleteKnowledgeBase, listMessages, readQuality, waitForDocument } from "./support/api";

test("answer feedback updates the retrieval quality view", async ({ page, api }, testInfo) => {
  test.setTimeout(240_000);
  const knowledgeBaseName = `e2e-quality-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  let knowledgeBaseId = "";
  const baseline = await readQuality(api);

  try {
    const knowledgeBase = await createKnowledgeBase(api, knowledgeBaseName);
    knowledgeBaseId = knowledgeBase.id;

    await page.goto(`/knowledge-bases/${knowledgeBaseId}`);
    await page.getByTestId("document-upload-input").setInputFiles(
      resolve(process.cwd(), "../examples/demo/retrieval-handbook.md"),
    );
    await waitForDocument(api, knowledgeBaseId, "retrieval-handbook.md");

    await page.getByTestId("kb-chat-toggle").click();
    await expect(page.getByTestId("kb-chat-panel")).toBeVisible();
    await page.getByTestId("session-new").click();
    await expect(page).toHaveURL(/\/chat\/[^/?#]+$/);
    const conversationId = new URL(page.url()).pathname.split("/").filter(Boolean).pop() ?? "";
    expect(conversationId).not.toBe("");

    await page.getByRole("textbox", { name: "问题" }).fill("how are keyword and vector results combined");
    await page.getByRole("button", { name: /提\s*问/ }).click();
    const answer = page.getByTestId("message-assistant").last();
    await expect(answer).toBeVisible({ timeout: 180_000 });
    await expect(answer.locator(".answer")).not.toBeEmpty();
    await expect(page.getByTestId("message-streaming")).toBeHidden({ timeout: 180_000 });

    const messages = await listMessages(api, conversationId);
    const assistant = [...messages].reverse().find((item) => item.message.role === "assistant");
    expect(assistant).toBeTruthy();
    expect(assistant!.message.id).not.toBe("");

    await page.getByTestId("message-feedback-helpful").last().click();
    await expect(page.getByText("感谢反馈")).toBeVisible({ timeout: 10_000 });
    await expect.poll(async () => (await readQuality(api)).helpful, { timeout: 10_000 }).toBe(baseline.helpful + 1);
    const afterHelpful = await readQuality(api);
    expect(afterHelpful.unhelpful).toBe(baseline.unhelpful);

    await page.getByTestId("message-feedback-unhelpful").last().click();
    await expect.poll(async () => (await readQuality(api)).unhelpful, { timeout: 10_000 }).toBe(baseline.unhelpful + 1);
    const afterUnhelpful = await readQuality(api);
    expect(afterUnhelpful.helpful).toBe(baseline.helpful);

    await page.getByTestId("citation-list").first().getByRole("button").first().click();
    await expect(page).toHaveURL(/\/documents\//);
    await expect.poll(async () => (await readQuality(api)).citationClicks, { timeout: 10_000 }).toBeGreaterThanOrEqual(baseline.citationClicks + 1);

    await page.goto("/ops");
    await page.getByRole("tab", { name: "检索质量" }).click();
    await expect(page.getByTestId("ops-quality")).toBeVisible();
    await expect(page.getByTestId("ops-quality-unhelpful")).toHaveText(String(afterUnhelpful.unhelpful));
    await page.screenshot({ path: testInfo.outputPath("quality-feedback.png") });
  } finally {
    if (knowledgeBaseId) {
      await deleteKnowledgeBase(api, knowledgeBaseId).catch(() => undefined);
    }
  }
});
