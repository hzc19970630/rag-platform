import { test, expect } from "./support/fixtures";
import { deleteKnowledgeBase, deletePreset } from "./support/api";

test("preset packs can be inspected, cloned, edited and applied to a knowledge base", async ({ page, api }, testInfo) => {
  test.setTimeout(180_000);
  const suffix = Date.now();
  const customName = `e2e-pack-${suffix}`;
  const knowledgeBaseName = `e2e-preset-kb-${suffix}`;
  let knowledgeBaseId = "";

  try {
    await page.goto("/admin");
    await page.getByRole("tab", { name: "预置包" }).click();
    const builtin = page.getByRole("row").filter({ hasText: "general-enterprise@1.0.0" });
    await expect(builtin).toBeVisible();
    await expect(builtin.getByTestId("preset-edit")).toHaveCount(0);
    await expect(builtin.getByTestId("preset-delete")).toHaveCount(0);

    await builtin.getByTestId("preset-clone").click();
    const cloneDialog = page.getByRole("dialog").filter({ hasText: "克隆为租户自定义包" });
    await cloneDialog.getByLabel("新包名").fill(customName);
    await cloneDialog.getByRole("button", { name: /克\s*隆/ }).click();
    const custom = page.getByRole("row").filter({ hasText: `${customName}@1.0.0` });
    await expect(custom).toBeVisible({ timeout: 10_000 });
    await expect(custom.getByTestId("preset-edit")).toBeVisible();

    await custom.getByTestId("preset-edit").click();
    const editor = page.getByRole("dialog").filter({ hasText: "编辑租户预置包" });
    const textarea = editor.locator("textarea");
    await expect(textarea).toContainText("EnterprisePresetV1");
    const manifest = (await textarea.inputValue()).replace('"top_k": 8', '"top_k": 5');
    await textarea.fill(manifest);
    await editor.getByRole("button", { name: /保\s*存/ }).click();
    await expect(page.getByText("预置包已保存")).toBeVisible({ timeout: 10_000 });

    await custom.getByTestId("preset-view").click();
    const detail = page.getByRole("dialog").filter({ hasText: "预置包详情" });
    await expect(detail).toContainText('"top_k": 5');
    await detail.getByRole("button", { name: "关 闭" }).click();

    await page.goto("/knowledge-bases");
    await page.getByTestId("kb-create-open").click();
    const createDialog = page.getByRole("dialog");
    await createDialog.getByTestId("kb-name-input").fill(knowledgeBaseName);
    await createDialog.getByTestId("kb-preset-select").click();
    await page.locator(".ant-select-item-option").filter({ hasText: customName }).click();
    await expect(createDialog.getByTestId("kb-preset-info")).toBeVisible();
    await createDialog.getByRole("button", { name: /创\s*建/ }).click();
    await expect(page).toHaveURL(/\/knowledge-bases\/[^/?#]+$/);
    knowledgeBaseId = new URL(page.url()).pathname.split("/").filter(Boolean).pop() ?? "";
    const configuration = await api.get(`knowledge-bases/${knowledgeBaseId}/configuration`);
    expect(configuration.ok()).toBeTruthy();
    const body = (await configuration.json()) as {
      retrieval: { effective: { top_k: number }; fields: { top_k: { source: string } } };
      presetName?: string;
      presetVersion?: string;
    };
    expect(body.retrieval.effective.top_k).toBe(5);
    expect(body.retrieval.fields.top_k.source).toBe("knowledge_base");
    expect(body.presetName).toBe(customName);
    expect(body.presetVersion).toBe("1.0.0");
    await page.screenshot({ path: testInfo.outputPath("preset-applied.png") });

    await page.goto("/admin");
    await page.getByRole("tab", { name: "预置包" }).click();
    await page.getByRole("row").filter({ hasText: `${customName}@1.0.0` }).getByTestId("preset-delete").click();
    await page.locator(".ant-popconfirm .ant-btn-primary").click();
    await expect(page.getByRole("row").filter({ hasText: `${customName}@1.0.0` })).toHaveCount(0, { timeout: 10_000 });
    await expect(page.getByRole("row").filter({ hasText: "general-enterprise@1.0.0" })).toBeVisible();
  } finally {
    if (knowledgeBaseId) {
      await deleteKnowledgeBase(api, knowledgeBaseId).catch(() => undefined);
    }
    await deletePreset(api, customName, "1.0.0").catch(() => undefined);
  }
});
