import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { configurationFixture } from "../../test/fixtures";
import { SystemInfoSection } from "./SystemInfoSection";

describe("SystemInfoSection", () => {
  it("shows environment values but no editable controls", () => {
    render(<SystemInfoSection configuration={configurationFixture()} />);

    expect(screen.getByText(/bge-m3/)).toBeInTheDocument();
    expect(screen.getByText(/qwen-plus/)).toBeInTheDocument();
    expect(screen.getByText(/docreader-m2-v2/)).toBeInTheDocument();
    expect(screen.queryAllByRole("textbox")).toHaveLength(0);
    expect(screen.queryAllByRole("spinbutton")).toHaveLength(0);
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
    expect(screen.queryAllByRole("combobox")).toHaveLength(0);
  });
});