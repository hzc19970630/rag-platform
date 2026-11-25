import { App } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../api/client";
import type { TableAskResult, TableAskSQLResult } from "../../api/types";
import { TableQaDrawer } from "./TableQaDrawer";

vi.mock("../../api/client", () => ({
  api: vi.fn(),
  jsonInit: (body: unknown) => ({ body: JSON.stringify(body) }),
}));

vi.mock("../../api/hooks", () => ({
  useTableAssets: () => ({
    data: [{ id: "asset-1", sheet: "Sheet1", rowCount: 3 }],
    isLoading: false,
  }),
}));

const answer: TableAskResult = {
  route: "aggregate",
  operation: "sum",
  confidence: 0.92,
  matchedRules: ["aggregate.sum"],
  assetId: "asset-1",
  documentId: "doc-a",
  sheet: "Sheet1",
  header: ["订单号", "客户", "数量"],
  column: "数量",
  rows: [
    { rowNumber: 2, cells: { 订单号: "A-100", 客户: "张三", 数量: "2" } },
    { rowNumber: 4, cells: { 订单号: "A-102", 客户: "张三", 数量: "3" } },
  ],
  matchedRows: 2,
  totalRows: 3,
  value: 5,
  notes: [],
};

const preview: TableAskSQLResult = {
  preview: true,
  sql: "SELECT row_number, cells FROM table_rows WHERE table_asset_id = 'asset-1' LIMIT 200",
  plan: {
    operation: "sum",
    column: "数量",
    filters: [{ column: "客户", op: "eq", value: "张三" }],
    confidence: 0.9,
  },
  estimatedRows: 2,
  fallback: false,
  matchedRows: 0,
  elapsedMs: 4,
  notes: [],
};

const executed: TableAskSQLResult = {
  ...preview,
  preview: false,
  result: { ...answer, value: 5 },
  matchedRows: 2,
  elapsedMs: 12,
};

const fallbackPreview: TableAskSQLResult = {
  preview: true,
  sql: "",
  estimatedRows: 0,
  fallback: true,
  fallbackReason: "low_confidence",
  matchedRows: 0,
  elapsedMs: 3,
  notes: ["fallback to deterministic table filtering: low_confidence"],
};

function renderDrawer() {
  return render(
    <App>
      <TableQaDrawer open onClose={() => undefined} knowledgeBaseId="kb-a" />
    </App>,
  );
}

function ask(question: string) {
  fireEvent.change(screen.getByPlaceholderText(/例如/), { target: { value: question } });
}

describe("TableQaDrawer", () => {
  const mocked = vi.mocked(api);
  beforeEach(() => mocked.mockReset());

  it("shows deterministic answers with sheet and row citations", async () => {
    mocked.mockResolvedValue(answer);
    renderDrawer();
    ask("张三的数量总和是多少");
    fireEvent.click(screen.getByRole("button", { name: /提\s*问/ }));
    expect(await screen.findByText("A-100")).toBeInTheDocument();
    expect(screen.getAllByText("张三")).toHaveLength(2);
    expect(screen.getAllByText("Sheet1 · 行号").length).toBeGreaterThan(0);
    expect(screen.getByText("#2")).toBeInTheDocument();
    expect(screen.getByText("5")).toBeInTheDocument();
  });

  it("previews SQL and executes it on demand", async () => {
    mocked.mockImplementation((async (...args: unknown[]) => {
      const [path, init] = args as [string, RequestInit | undefined];
      if (String(path).endsWith("/tables/ask/sql")) {
        const body = JSON.parse(String(init?.body ?? "{}")) as { preview?: boolean };
        return body.preview ? preview : executed;
      }
      return answer;
    }) as never);
    renderDrawer();
    ask("张三的数量总和是多少");
    fireEvent.click(screen.getByRole("button", { name: /预览 SQL/ }));
    expect(await screen.findByText(/SELECT row_number/)).toBeInTheDocument();
    expect(screen.getByText("预计影响行数")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /执行 SQL/ }));
    await waitFor(() => expect(screen.getByText(/#4/)).toBeInTheDocument());
    expect(screen.getByText("A-102")).toBeInTheDocument();
  });

  it("marks a rejected plan as a deterministic fallback without an execute action", async () => {
    mocked.mockResolvedValue(fallbackPreview);
    renderDrawer();
    ask("张三的数量总和是多少");
    fireEvent.click(screen.getByRole("button", { name: /预览 SQL/ }));
    expect(await screen.findByText("已回退到确定性筛选")).toBeInTheDocument();
    expect(screen.getByText(/low_confidence/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /执行 SQL/ })).not.toBeInTheDocument();
  });
});
