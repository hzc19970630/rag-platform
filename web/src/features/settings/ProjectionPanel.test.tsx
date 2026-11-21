import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntdApp } from "antd";
import { render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ProjectionPanel } from "./ProjectionPanel";

const status = {
  knowledgeBaseId: "kb-1",
  joinStatus: "KNOWLEDGE_JOINED",
  wikiEnabled: true,
  graphEnabled: false,
  runs: [{
    id: "run-1",
    projectionType: "wiki",
    status: "FAILED",
    model: "chat-model",
    errorCode: "model_error",
    createdAt: "2026-11-19T00:00:00Z",
    updatedAt: "2026-11-19T00:00:00Z",
  }],
  relationRuns: [],
  pages: { total: 0, published: 0, pendingReview: 0, stale: 0, indexed: 0 },
  graph: { snapshots: 0, activeSnapshot: "", entities: 0, relations: 0, pendingReview: 0 },
};

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <AntdApp>
        <ProjectionPanel knowledgeBaseId="kb-1" canWrite={false} embedded />
      </AntdApp>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ProjectionPanel", () => {
  it("keeps run table columns fixed so narrow screens scroll horizontally", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(status), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));
    const { container } = renderPanel();

    await waitFor(() => expect(container.querySelector(".projection-runs .ant-table-content table")).toBeTruthy());
    const table = container.querySelector(".projection-runs .ant-table-content table") as HTMLTableElement;
    expect(table.style.width).toBe("850px");
    expect(table.style.tableLayout).toBe("fixed");
    const widths = [...table.querySelectorAll("colgroup col")].map((col) => (col as HTMLElement).style.width);
    expect(widths).toEqual(["110px", "240px", "180px", "150px", "170px"]);
  });

  it("shows the invalidation gate instead of pretending the projection is current", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      ...status,
      joinStatus: "KNOWLEDGE_PARTIAL",
      pendingChanges: 3,
      targetEpoch: 7,
      wikiAppliedEpoch: 4,
      relationAppliedEpoch: 4,
      invalidated: true,
    }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })));
    const { container } = renderPanel();
    await waitFor(() => expect(container.textContent).toContain("投影已失效"));
    expect(container.textContent).toContain("重建完成前这些内容不可用");
  });
});