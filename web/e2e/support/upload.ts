import { expect, type Page } from "@playwright/test";

// B30 replaced the single-file toast with the upload queue modal. Tests must
// wait for the job to finish and dismiss the modal before touching the page.
export async function uploadAndWait(page: Page, filePath: string): Promise<void> {
  await page.getByTestId("document-upload-input").setInputFiles(filePath);
  const job = page.getByTestId("upload-job").first();
  await expect(job).toContainText("已完成", { timeout: 120_000 });
  const dialog = page.getByRole("dialog");
  const continueButton = dialog.getByRole("button", { name: "后台继续" });
  if (await continueButton.isVisible().catch(() => false)) {
    await continueButton.click();
  }
}
