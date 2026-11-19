import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { FolderTree } from "./FolderTree";

const folder = {
  id: "fld-1", knowledgeBaseId: "kb-1", parentId: "", name: "Contracts",
  path: "/fld-1/", depth: 1, createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z",
};

function renderTree(props: Partial<Parameters<typeof FolderTree>[0]> = {}) {
  return render(
    <FolderTree
      folders={[]}
      knowledgeBaseName="Test KB"
      selectedKey="all"
      canWrite
      canDelete
      onSelect={vi.fn()}
      onCreate={vi.fn().mockResolvedValue(undefined)}
      onRename={vi.fn()}
      onMove={vi.fn()}
      onDelete={vi.fn()}
      onDownload={vi.fn()}
      onBatchDelete={vi.fn()}
      {...props}
    />,
  );
}

describe("FolderTree", () => {
  it("renders the knowledge base root and its folders", () => {
    renderTree({ folders: [folder] });
    expect(screen.getByText("Test KB")).toBeInTheDocument();
    expect(screen.getByText("Contracts")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "新建子文件夹" })).toHaveLength(2);
  });

  it("selects the root filter when the knowledge base node is clicked", () => {
    const onSelect = vi.fn();
    renderTree({ onSelect });
    fireEvent.click(screen.getByText("Test KB"));
    expect(onSelect).toHaveBeenCalledWith("root");
  });

  it("creates a folder inline at the root without a dialog", async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    renderTree({ onCreate });
    fireEvent.click(screen.getByRole("button", { name: "新建文件夹" }));
    const input = screen.getByRole("textbox", { name: "新建文件夹" });
    fireEvent.change(input, { target: { value: "Finance" } });
    fireEvent.click(screen.getByRole("button", { name: "确认" }));
    await waitFor(() => expect(onCreate).toHaveBeenCalledWith("", "Finance"));
    await waitFor(() => expect(screen.queryByRole("textbox", { name: "新建文件夹" })).toBeNull());
  });

  it("creates a child folder under the hovered folder", async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    renderTree({ folders: [folder], onCreate });
    const createChild = screen.getAllByRole("button", { name: "新建子文件夹" });
    fireEvent.click(createChild[createChild.length - 1]);
    const input = screen.getByRole("textbox", { name: "新建文件夹" });
    fireEvent.change(input, { target: { value: "Sub" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(onCreate).toHaveBeenCalledWith("fld-1", "Sub"));
  });

  it("enters edit mode", () => {
    renderTree({ folders: [folder] });
    fireEvent.click(screen.getByRole("button", { name: /编\s*辑/ }));
    expect(screen.getByText("已选 0 项")).toBeInTheDocument();
  });

  it("filters folders by name", () => {
    renderTree({ folders: [folder] });
    const search = screen.getByPlaceholderText("搜索文件夹");
    fireEvent.change(search, { target: { value: "zzz" } });
    expect(screen.getByText("没有匹配的文件夹。")).toBeInTheDocument();
    fireEvent.change(search, { target: { value: "con" } });
    expect(screen.getByText("Contracts")).toBeInTheDocument();
  });
});