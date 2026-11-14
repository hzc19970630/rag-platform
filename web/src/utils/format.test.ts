import { describe, expect, it } from "vitest";
import { formatSize, formatTime, parseIndexingStrategy } from "./format";

describe("formatTime", () => {
  it("returns a dash for missing values", () => {
    expect(formatTime()).toBe("-");
    expect(formatTime("")).toBe("-");
  });

  it("returns the raw value for invalid dates", () => {
    expect(formatTime("not-a-date")).toBe("not-a-date");
  });

  it("formats a valid timestamp", () => {
    expect(formatTime("2026-11-14T10:00:00Z")).not.toBe("2026-11-14T10:00:00Z");
  });
});

describe("formatSize", () => {
  it("scales through every unit", () => {
    expect(formatSize(512)).toBe("512 B");
    expect(formatSize(2048)).toBe("2.0 KB");
    expect(formatSize(5 * 1024 * 1024)).toBe("5.0 MB");
    expect(formatSize(3 * 1024 * 1024 * 1024)).toBe("3.0 GB");
    expect(formatSize(2 * 1024 * 1024 * 1024 * 1024)).toBe("2.0 TB");
  });
});

describe("parseIndexingStrategy", () => {
  it("normalizes every switch and defaults capabilities", () => {
    expect(parseIndexingStrategy(undefined)).toBeUndefined();
    expect(parseIndexingStrategy("")).toBeUndefined();
    expect(parseIndexingStrategy("{bad json")).toBeUndefined();
    expect(parseIndexingStrategy(`{"vector_enabled":true,"keyword_enabled":1,"wiki_enabled":false,"graph_enabled":"yes","required_capabilities":["evidence","graph"]}`)).toEqual({
      vector_enabled: true,
      keyword_enabled: true,
      wiki_enabled: false,
      graph_enabled: true,
      required_capabilities: ["evidence", "graph"],
    });
    expect(parseIndexingStrategy(`{}`)).toEqual({
      vector_enabled: false,
      keyword_enabled: false,
      wiki_enabled: false,
      graph_enabled: false,
      required_capabilities: [],
    });
    expect(parseIndexingStrategy(`{"required_capabilities":"evidence"}`)?.required_capabilities).toEqual([]);
  });
});
