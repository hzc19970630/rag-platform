import { test, expect } from "./support/fixtures";
import { createKnowledgeBase, deleteKnowledgeBase } from "./support/api";

test("creates folders inline at the root and under a folder", async ({ page, api }) => {
  const knowledgeBase = await createKnowledgeBase(api, `e2e-folders-${Date.now()}`);
  let knowledgeBaseId = knowledgeBase.id;

  try {
    await page.goto(`/knowledge-bases/${knowledgeBase.id}`);
    const tree = page.locator(".folder-tree-scroll");
    await expect(tree.getByText(knowledgeBase.name)).toBeVisible();

    await page.getByRole("button", { name: "新建文件夹" }).click();
    await expect(page.locator(".ant-modal")).toHaveCount(0);
    const rootInput = page.getByRole("textbox", { name: "新建文件夹" });
    await expect(rootInput).toBeVisible();
    await rootInput.fill("Contracts");
    await page.getByRole("button", { name: "确认" }).click();
    await expect(tree.getByText("Contracts")).toBeVisible();

    const folderRow = page.locator(".ant-tree-treenode").filter({ hasText: "Contracts" });
    await folderRow.hover();
    await folderRow.getByRole("button", { name: "新建子文件夹" }).click();
    const childInput = page.getByRole("textbox", { name: "新建文件夹" });
    await expect(childInput).toBeVisible();
    await childInput.fill("2026");
    await page.keyboard.press("Enter");
    await expect(tree.getByText("2026")).toBeVisible();

    const response = await api.get(`knowledge-bases/${knowledgeBaseId}/folders`);
    const body = (await response.json()) as { items: Array<{ id: string; name: string; parentId: string }> };
    const contracts = body.items.find((item) => item.name === "Contracts");
    const child = body.items.find((item) => item.name === "2026");
    expect(contracts).toBeTruthy();
    expect(child?.parentId).toBe(contracts!.id);

    await folderRow.hover();
    await folderRow.getByRole("button", { name: "文件夹操作" }).click();
    await page.getByRole("menuitem", { name: "移动" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("combobox", { name: "上级文件夹" }).click();
    const nodes = page.locator(".ant-select-tree-title");
    await expect(nodes.filter({ hasText: knowledgeBase.name })).toBeVisible();
    await expect(nodes.filter({ hasText: "Contracts" })).toBeVisible();
    await expect(nodes.filter({ hasText: "2026" })).toBeVisible();
    await page.keyboard.press("Escape");
  } finally {
    if (knowledgeBaseId) await deleteKnowledgeBase(api, knowledgeBaseId);
    knowledgeBaseId = "";
  }
});