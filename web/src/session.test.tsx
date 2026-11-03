import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";
import { usePermissions } from "./session";
import { SessionProvider } from "./session";
import { meWith } from "./test/fixtures";

function wrapper(capabilities: string[]) {
  return ({ children }: { children: ReactNode }) => (
    <SessionProvider value={meWith(capabilities)}>{children}</SessionProvider>
  );
}

describe("usePermissions", () => {
  it("grants viewer read access and question-asking only", () => {
    const { result } = renderHook(() => usePermissions(), {
      wrapper: wrapper(["knowledge_base:read"]),
    });
    expect(result.current.canRead).toBe(true);
    expect(result.current.canWrite).toBe(false);
    expect(result.current.canManage).toBe(false);
    expect(result.current.canDelete).toBe(false);
    expect(result.current.canChat).toBe(true);
    expect(result.current.canOperate).toBe(false);
  });

  it("grants editor write access without member management", () => {
    const { result } = renderHook(() => usePermissions(), {
      wrapper: wrapper(["knowledge_base:read", "knowledge_base:write"]),
    });
    expect(result.current.canWrite).toBe(true);
    expect(result.current.canManage).toBe(false);
    expect(result.current.canOperate).toBe(false);
  });

  it("grants operators the operations surface", () => {
    const { result } = renderHook(() => usePermissions(), {
      wrapper: wrapper([
        "knowledge_base:read",
        "knowledge_base:write",
        "knowledge_base:delete",
        "knowledge_base:manage_members",
      ]),
    });
    expect(result.current.canManage).toBe(true);
    expect(result.current.canDelete).toBe(true);
    expect(result.current.canOperate).toBe(true);
  });
});