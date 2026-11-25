import { test, expect } from "./support/fixtures";
import { createKnowledgeBase, deleteKnowledgeBase, waitForDocument } from "./support/api";

const filename = "table-qa.csv";
const csv = [
  "订单号,客户,数量,金额",
  "A-100,张三,2,10",
  "A-101,李四,5,30",
  "A-102,张三,3,20",
].join("\n");

test("asks a table question, previews SQL and cites rows", async ({ page, api }) => {
  test.setTimeout(180_000);
  const knowledgeBase = await createKnowledgeBase(api, `e2e-table-qa-${Date.now()}`);
  try {
    const upload = await api.post(`knowledge-bases/${knowledgeBase.id}/documents`, {
      multipart: {
        file: { name: filename, mimeType: "text/csv", buffer: Buffer.from(csv, "utf8") },
      },
    });
    expect(upload.status()).toBe(202);
    await waitForDocument(api, knowledgeBase.id, filename, 120_000);

    await page.goto(`/knowledge-bases/${knowledgeBase.id}`);
    await page.getByRole("button", { name: /刷\s*新/ }).click();
    const documentRow = page.getByTestId("document-name").filter({ hasText: filename });
    await expect(documentRow).toBeVisible({ timeout: 20_000 });
    await documentRow.locator("xpath=ancestor::tr").getByRole("button", { name: /详\s*情/ }).click();
    await expect(page.getByText("索引覆盖").first()).toBeVisible({ timeout: 20_000 });

    await page.getByRole("button", { name: "表格问答" }).click();
    const drawer = page.getByRole("dialog");
    await expect(drawer).toBeVisible();

    await drawer.getByPlaceholder(/例如/).fill("张三的数量总和是多少");
    await drawer.getByRole("button", { name: /提\s*问/ }).click();
    await expect(drawer.getByText("A-100")).toBeVisible({ timeout: 20_000 });
    await expect(drawer.getByText("A-102")).toBeVisible();
    await expect(drawer.getByRole("columnheader", { name: /· 行号/ })).toBeVisible();
    await expect(drawer.getByRole("heading", { name: "10", level: 4 })).toBeVisible();

    await drawer.getByPlaceholder(/例如/).fill("订单的数量总和是多少");
    await drawer.getByRole("button", { name: /预览 SQL/ }).click();
    if (process.env.E2E_EXPECT_SQL_PLAN === "1") {
      await expect(drawer.getByText(/SELECT SUM\(TRY_CAST\("数量" AS DOUBLE\)\)/)).toBeVisible({ timeout: 60_000 });
      await drawer.getByRole("button", { name: /执行 SQL/ }).click();
      await expect(drawer.getByRole("heading", { name: "10", level: 4 })).toBeVisible({ timeout: 30_000 });
    } else {
      await expect(drawer.getByText("已回退到确定性筛选")).toBeVisible({ timeout: 30_000 });
      await expect(drawer.getByText(/模型计划未通过校验/)).toBeVisible();
    }

    await page.setViewportSize({ width: 375, height: 812 });
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);

    await page.keyboard.press("Escape");
    await expect(drawer).toBeHidden();
  } finally {
    await deleteKnowledgeBase(api, knowledgeBase.id);
  }
});
