import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Conversation } from "../api/types";
import i18n from "../i18n";
import { SessionList } from "./SessionList";

// jsdom has no layout, so the virtualizer would render an empty viewport.
vi.mock("./VirtualSessionList", () => ({
  VirtualSessionList: <Row extends { key: string }>(props: { rows: Row[]; renderRow: (row: Row) => unknown }) => (
    <div>{props.rows.map((row) => <div key={row.key}>{props.renderRow(row) as never}</div>)}</div>
  ),
}));

function conversation(id: string, updatedAt: string, knowledgeBaseId = "kb-1", title = ""): Conversation {
  return {
    id,
    knowledgeBaseId,
    title,
    createdAt: updatedAt,
    updatedAt,
  } as Conversation;
}

describe("SessionList", () => {
  it("sorts conversations by updatedAt descending and opens one on click", () => {
    const onSelect = vi.fn();
    const items = [
      conversation("older", "2026-11-01T00:00:00Z", "kb-1", "Older"),
      conversation("newer", "2026-11-18T00:00:00Z", "kb-1", "Newer"),
    ];
    render(<SessionList items={items} onSelect={onSelect} onCreate={() => undefined} />);
    const titles = screen.getAllByText(/Newer|Older/).map((node) => node.textContent);
    expect(titles).toEqual(["Newer", "Older"]);
    fireEvent.click(screen.getByText("Older").closest("button")!);
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ id: "older" }));
  });

  it("offers creation from the empty state", () => {
    const onCreate = vi.fn();
    render(<SessionList items={[]} onSelect={() => undefined} onCreate={onCreate} />);
    expect(screen.getByText(i18n.t("chat.noConversations"))).toBeInTheDocument();
    fireEvent.click(screen.getByText(i18n.t("chat.newConversation")));
    expect(onCreate).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByTestId("session-new"));
    expect(onCreate).toHaveBeenCalledTimes(2);
  });

  it("groups conversations by knowledge base when asked to", () => {
    const items = [
      conversation("a", "2026-11-18T00:00:00Z", "kb-1", "Alpha"),
      conversation("b", "2026-11-17T00:00:00Z", "kb-2", "Beta"),
    ];
    const names = new Map([["kb-1", "First KB"], ["kb-2", "Second KB"]]);
    render(<SessionList items={items} grouped knowledgeBaseNames={names} onSelect={() => undefined} onCreate={() => undefined} />);
    expect(screen.getByText("First KB")).toBeInTheDocument();
    expect(screen.getByText("Second KB")).toBeInTheDocument();
  });
});
