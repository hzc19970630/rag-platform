import { describe, expect, it } from "vitest";
import { friendlyApiMessage } from "./errorMessages";

describe("friendlyApiMessage", () => {
  it("prefers a known validation message over the generic code", () => {
    expect(friendlyApiMessage("invalid_request", "name is required")).toBe("名称不能为空。");
  });

  it("maps stable error codes to user-facing copy", () => {
    expect(friendlyApiMessage("quota_exceeded", "quota exceeded")).toBe("已达到配额上限，请联系管理员。");
  });

  it("never leaks an unknown backend message", () => {
    expect(friendlyApiMessage("weird_code", "boom: internal detail")).toBe("操作失败，请稍后重试。");
  });
});