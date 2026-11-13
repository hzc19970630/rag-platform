import { api, jsonInit } from "./client";
import type { ConfigChangePreview, ConfigChangePreviewRequest } from "./types";

// previewConfigChange is read-only: the server never persists or rebuilds.
export function previewConfigChange(input: ConfigChangePreviewRequest): Promise<ConfigChangePreview> {
  return api<ConfigChangePreview>("/config/preview", jsonInit(input, "POST"));
}
