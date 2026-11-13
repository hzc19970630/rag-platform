import { resolve } from "node:path";
import { test, expect } from "./support/fixtures";
import { deleteKnowledgeBase, searchEvidence, waitForDocument } from "./support/api";

const filename = "retrieval-handbook.md";
const fixturePath = resolve(process.cwd(), "../examples/demo/retrieval-handbook.md");

test("upload, parse, search, answer and delete document", async ({ page, api }) => {
  test.setTimeout(240_000);
  const knowledgeBaseName = `e2e-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  let knowledgeBaseId = "";

  try {
    await page.goto("/knowledge-bases");
    await expect(page.getByRole("heading", { name: "知识库", level: 2 })).toBeVisible();

    await page.getByTestId("kb-create-open").click();
    const createDialog = page.getByRole("dialog");
    await expect(createDialog).toBeVisible();
    await createDialog.locator("input").first().fill(knowledgeBaseName);
    await createDialog.getByRole("button", { name: /创\s*建/ }).click();

    await expect(page).toHaveURL(/\/knowledge-bases\/[^/?#]+$/);
    await expect(page.getByRole("heading", { name: knowledgeBaseName, level: 2 })).toBeVisible();
    knowledgeBaseId = new URL(page.url()).pathname.split("/").filter(Boolean).pop() ?? "";
    expect(knowledgeBaseId).not.toBe("");

    await page.getByTestId("document-upload-input").setInputFiles(fixturePath);
    await expect(page.getByText(new RegExp(`已上传 ${filename}`))).toBeVisible({ timeout: 15_000 });
    await waitForDocument(api, knowledgeBaseId, filename);

    await page.getByRole("button", { name: /刷\s*新/ }).click();
    const documentRow = page.getByTestId("document-name").filter({ hasText: filename }).locator("xpath=ancestor::tr");
    await expect(documentRow).toBeVisible();
    await expect(documentRow).toContainText(/证据就绪|EVIDENCE_READY/);

    const evidence = await searchEvidence(api, knowledgeBaseId, "how are keyword and vector results combined");
    expect(evidence.schemaVersion).toBe("EvidenceSetV1");
    expect(evidence.items.length).toBeGreaterThan(0);
    expect(evidence.plan.intent).toBe("fact");
    expect(evidence.plan.vectorEnabled).toBe(true);
    expect(evidence.plan.keywordEnabled).toBe(true);

    await page.getByRole("button", { name: /问\s*答/ }).click();
    await expect(page).toHaveURL(/\/chat\/[^/?#]+$/);

    await page.getByRole("textbox", { name: "问题" }).fill(
      "how does hybrid recall combine keyword and vector results",
    );
    await page.getByRole("button", { name: /提\s*问/ }).click();

    const answer = page.getByTestId("message-assistant").last();
    await expect(answer).toBeVisible({ timeout: 180_000 });
    await expect(answer.locator(".answer")).not.toBeEmpty();
    await expect(answer.getByTestId("citation-list").first()).toBeVisible();
    await expect(page.getByTestId("message-streaming")).toBeHidden({ timeout: 180_000 });

    await answer.getByTestId("citation-list").getByRole("button").first().click();
    await expect(page).toHaveURL(/\/documents\//);

    await page.goto(`/knowledge-bases/${knowledgeBaseId}`);
    const row = page.getByTestId("document-name").filter({ hasText: filename }).locator("xpath=ancestor::tr");
    await expect(row).toBeVisible();
    await row.getByRole("button", { name: /删\s*除/ }).click();
    const confirm = page.getByRole("dialog").filter({ hasText: "删除文档会移除" });
    await confirm.getByRole("button", { name: /删\s*除/ }).click();
    await expect(page.getByText(new RegExp(`已删除 ${filename}`))).toBeVisible();
    await expect(page.getByTestId("document-name")).toHaveCount(0);
  } finally {
    if (knowledgeBaseId) {
      try {
        await deleteKnowledgeBase(api, knowledgeBaseId);
      } catch (error) {
        console.warn(`cleanup knowledge base ${knowledgeBaseId} failed: ${String(error)}`);
      }
    }
  }
});
