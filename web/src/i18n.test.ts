import { describe, expect, it } from "vitest";
import i18n from "./i18n";

function keyPaths(value: unknown, prefix = ""): string[] {
  if (!value || typeof value !== "object") return [prefix];
  return Object.entries(value as Record<string, unknown>).flatMap(([key, child]) => (
    keyPaths(child, prefix ? `${prefix}.${key}` : key)
  ));
}

function bundle(language: string): unknown {
  return i18n.getResourceBundle(language, "translation") ?? {};
}

describe("translation resources", () => {
  it("keeps the zh-CN and en-US key sets in sync", () => {
    expect(keyPaths(bundle("en-US")).sort()).toEqual(keyPaths(bundle("zh-CN")).sort());
  });
});