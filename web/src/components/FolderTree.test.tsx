import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { FolderTree } from "./FolderTree";

const folder = {
  id: "fld-1", knowledgeBaseId: "kb-1", parentId: "", name: "Contracts",
  path: "/fld-1/", depth: 1, createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z",
};

describe("FolderTree", () => {
  it("renders the folder list", () => {
    render(
      <FolderTree
        folders={[folder]}
        selectedKey="all"
        canWrite
        canDelete
        onSelect={vi.fn()}
        onCreate={vi.fn()}
        onRename={vi.fn()}
        onMove={vi.fn()}
        onDelete={vi.fn()}
        onDownload={vi.fn()}
        onBatchDelete={vi.fn()}
      />,
    );
    expect(document.body.textContent).toContain("Contracts");
    expect(screen.getByRole("button", { name: "新建子文件夹" })).toBeInTheDocument();
  });

  it("enters edit mode", () => {
    render(
      <FolderTree
        folders={[folder]}
        selectedKey="all"
        canWrite
        canDelete
        onSelect={vi.fn()}
        onCreate={vi.fn()}
        onRename={vi.fn()}
        onMove={vi.fn()}
        onDelete={vi.fn()}
        onDownload={vi.fn()}
        onBatchDelete={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /编\s*辑/ }));
    expect(screen.getByText("已选 0 项")).toBeInTheDocument();
  });

  it("filters folders by name", () => {
    render(
      <FolderTree
        folders={[folder]}
        selectedKey="all"
        canWrite
        canDelete
        onSelect={vi.fn()}
        onCreate={vi.fn()}
        onRename={vi.fn()}
        onMove={vi.fn()}
        onDelete={vi.fn()}
        onDownload={vi.fn()}
        onBatchDelete={vi.fn()}
      />,
    );
    const search = screen.getByPlaceholderText("搜索文件夹");
    fireEvent.change(search, { target: { value: "zzz" } });
    expect(screen.getByText("没有匹配的文件夹。")).toBeInTheDocument();
    fireEvent.change(search, { target: { value: "con" } });
    expect(screen.getByText("Contracts")).toBeInTheDocument();
  });
});