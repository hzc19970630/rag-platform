import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "antd";
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { IndexCoverageCard } from "./IndexCoverageCard";

const estimate = vi.fn();

vi.mock("../../api/hooks", () => ({
  queryKeys: {
    documentStatus: (id: string) => ["document-status", id],
    documentVersions: (id: string) => ["document-versions", id],
  },
  useDocumentStatus: () => ({ data: { contentReady: true } }),
  useDocumentIndexEstimate: () => ({ data: estimate() }),
}));

vi.mock("../../api/client", () => ({
  api: vi.fn(),
  jsonInit: (body: unknown) => ({ body: JSON.stringify(body) }),
}));

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return (
    <QueryClientProvider client={client}>
      <App>{children}</App>
    </QueryClientProvider>
  );
}

function estimateFixture(overrides: Record<string, unknown> = {}) {
  return {
    documentId: "doc-a",
    documentVersionId: "ver-a",
    indexMode: "standard",
    storageMode: "full",
    parseStatus: "parsed",
    parseTruncated: false,
    coverageKnown: true,
    tableCount: 65,
    totalRows: 975065,
    parsedRows: 975065,
    keywordRows: 975065,
    vectorRows: 2860,
    totalSheets: 65,
    parsedSheets: 65,
    vectorSheets: 65,
    currentVectorTokens: 248684,
    deepEstimatedTokens: 25000000,
    deepEstimatedMinutes: 438,
    tpmBudget: 0,
    sheets: [{ sheet: "Sheet65", storageMode: "full", parseStatus: "parsed", indexMode: "standard", totalRows: 15001, parsedRows: 15000, columnCount: 2, truncated: false }],
    ...overrides,
  };
}

describe("IndexCoverageCard", () => {
  beforeEach(() => estimate.mockReset());

  it("shows parse and vector coverage separately for a full document", () => {
    estimate.mockReturnValue(estimateFixture());
    render(<IndexCoverageCard documentId="doc-a" canWrite />, { wrapper });

    expect(screen.getByText(/已处理 65\/65 张表/)).toBeInTheDocument();
    expect(screen.getByText(/已向量化 2860\/975065 行/)).toBeInTheDocument();
    expect(screen.queryByText(/存在未完整索引的内容/)).not.toBeInTheDocument();
  });

  it("warns and disables row indexing when the document is sampled", () => {
    estimate.mockReturnValue(estimateFixture({
      storageMode: "sampled",
      parseStatus: "partial",
      parseTruncated: true,
      parsedRows: 197600,
      keywordRows: 197600,
      vectorRows: 2860,
    }));
    render(<IndexCoverageCard documentId="doc-a" canWrite />, { wrapper });

    expect(screen.getByText(/存在未完整索引的内容/)).toBeInTheDocument();
    expect(screen.getByText(/197600\/975065 行/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "深度索引全部" })).toBeDisabled();
  });
});
