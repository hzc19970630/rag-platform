import i18n from "../i18n";

// Backend error envelopes pair a stable code with a developer-facing message.
// Users see a localized sentence; the raw pair stays in ApiError.detail.
const MESSAGE_KEYS: Record<string, string> = {
  "name is required": "errors.validation.nameRequired",
  "name cannot be empty": "errors.validation.nameRequired",
  "file is required": "errors.validation.fileRequired",
  "filename is required": "errors.validation.filenameRequired",
  "file exceeds maximum size": "errors.validation.fileTooLarge",
  "unsupported mime type": "errors.validation.unsupportedMime",
  "expiresAt must be in the future": "errors.validation.expiryFuture",
  "unsupported knowledge base role": "errors.validation.unsupportedRole",
  "knowledge base scope contains an unsupported knowledge base": "errors.validation.kbScope",
};

const CODE_KEYS: Record<string, string> = {
  invalid_request: "errors.codes.invalid_request",
  unauthenticated: "errors.codes.unauthenticated",
  forbidden: "errors.codes.forbidden",
  not_found: "errors.codes.not_found",
  conflict: "errors.codes.conflict",
  not_ready: "errors.codes.not_ready",
  rate_limited: "errors.codes.rate_limited",
  quota_exceeded: "errors.codes.quota_exceeded",
  content_unavailable: "errors.codes.content_unavailable",
  payload_too_large: "errors.codes.payload_too_large",
  timeout: "errors.timeout",
  internal_error: "errors.codes.internal_error",
};

function lookup(key: string): string {
  if (!key) return "";
  const value = i18n.t(key);
  return value === key ? "" : value;
}

export function friendlyApiMessage(code: string, rawMessage: string): string {
  return lookup(MESSAGE_KEYS[rawMessage])
    || lookup(CODE_KEYS[code])
    || lookup("errors.fallback")
    || "操作失败，请稍后重试。";
}

export function friendlyStatusMessage(status: number): string {
  return i18n.t("errors.requestFailed", { status });
}

export function friendlyTimeoutMessage(): string {
  return lookup("errors.timeout") || "请求超时，请重试。";
}

export function apiErrorDetail(code: string, rawMessage: string): string {
  return [code, rawMessage].filter(Boolean).join(": ");
}