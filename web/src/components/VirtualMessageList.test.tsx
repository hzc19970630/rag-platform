import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { VirtualMessageList } from "./VirtualMessageList";

const rows = Array.from({ length: 100 }, (_, index) => ({ key: `row-${index}`, label: `row-${index}` }));

describe("VirtualMessageList", () => {
  beforeEach(() => {
    Object.defineProperty(HTMLElement.prototype, "clientHeight", { configurable: true, get: () => 300 });
    Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 600 });
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      x: 0, y: 0, top: 0, left: 0, right: 600, bottom: 300, width: 600, height: 300,
      toJSON: () => ({}),
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
    delete (HTMLElement.prototype as { clientHeight?: number }).clientHeight;
    delete (HTMLElement.prototype as { clientWidth?: number }).clientWidth;
  });

  it("renders only the visible window of a long message list", async () => {
    render(
      <VirtualMessageList
        rows={rows}
        estimateSize={50}
        renderRow={(row) => <div data-testid="message-row">{row.label}</div>}
      />,
    );

    await waitFor(() => {
      expect(screen.getAllByTestId("message-row").length).toBeGreaterThan(0);
    });
    const rendered = screen.getAllByTestId("message-row").length;
    expect(rendered).toBeLessThan(rows.length);
    // The list auto-scrolls to the newest row, so the oldest rows stay outside
    // the window while the last one is present.
    expect(screen.queryByText("row-0")).not.toBeInTheDocument();
    expect(screen.getByText("row-99")).toBeInTheDocument();
  });
});
