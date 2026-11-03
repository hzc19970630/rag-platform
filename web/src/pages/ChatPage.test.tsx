import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Citation } from "../api/types";
import { AnswerText, CitationList } from "./ChatPage";

const citation: Citation = {
  index: 1,
  chunkId: "chunk-1",
  documentId: "doc-1",
  documentVersionId: "version-1",
  headingPath: "验收标准",
  pageNumber: 3,
  quote: "回答需要引用可访问的原文片段。",
  rank: 1,
  score: 0.82,
};

describe("chat citations", () => {
  it("renders answer markers as buttons only when the citation is registered", () => {
    const onOpen = vi.fn();
    render(<AnswerText content="结论[1]，未注册[2]。" citations={[citation]} onOpen={onOpen} />);

    fireEvent.click(screen.getByRole("button", { name: /\[1\]/ }));
    expect(onOpen).toHaveBeenCalledWith(citation);
    expect(screen.queryByRole("button", { name: /\[2\]/ })).toBeNull();
  });

  it("shows the citation heading, page, score and quote", () => {
    const onOpen = vi.fn();
    render(<CitationList citations={[citation]} onOpen={onOpen} />);

    expect(screen.getByText(/验收标准/)).toBeInTheDocument();
    expect(screen.getByText(/0\.820/)).toBeInTheDocument();
    expect(screen.getByText(/可访问的原文片段/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /验收标准/ }));
    expect(onOpen).toHaveBeenCalledWith(citation);
  });
});