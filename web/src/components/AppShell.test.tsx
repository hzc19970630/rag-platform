import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";
import { SessionProvider } from "../session";
import { meWith } from "../test/fixtures";
import { useNavigation } from "./AppShell";

function wrapper(capabilities: string[]) {
  return ({ children }: { children: ReactNode }) => (
    <SessionProvider value={meWith(capabilities)}>{children}</SessionProvider>
  );
}

function keys(items: ReturnType<typeof useNavigation>): string[] {
  return (items ?? [])
    .map((item) => (item && "key" in item ? String(item.key) : ""))
    .filter(Boolean);
}

describe("sidebar navigation", () => {
  it("shows knowledge bases and chat to a reader", () => {
    const { result } = renderHook(() => useNavigation(), {
      wrapper: wrapper(["knowledge_base:read"]),
    });
    expect(keys(result.current)).toEqual(["knowledge-bases", "chat"]);
  });

  it("adds administration and operations for tenant operators", () => {
    const { result } = renderHook(() => useNavigation(), {
      wrapper: wrapper(["knowledge_base:read", "knowledge_base:manage_members", "tenant:admin"]),
    });
    expect(keys(result.current)).toEqual(["knowledge-bases", "chat", "admin", "ops"]);
  });

  it("keeps tenant surfaces off for knowledge-base managers", () => {
    const { result } = renderHook(() => useNavigation(), {
      wrapper: wrapper(["knowledge_base:read", "knowledge_base:manage_members"]),
    });
    expect(keys(result.current)).toEqual(["knowledge-bases", "chat"]);
  });

  it("hides chat without knowledge-base read access", () => {
    const { result } = renderHook(() => useNavigation(), {
      wrapper: wrapper([]),
    });
    expect(keys(result.current)).toEqual(["knowledge-bases"]);
  });
});