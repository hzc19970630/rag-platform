import { useVirtualizer } from "@tanstack/react-virtual";
import { useEffect, useRef, type ReactNode } from "react";

export type VirtualRow = { key: string };

// The answer list is the only unbounded list in the app. Windowing keeps a
// long conversation from mounting every message and citation.
export function VirtualMessageList<Row extends VirtualRow>(props: {
  rows: Row[];
  renderRow: (row: Row) => ReactNode;
  estimateSize?: number;
  autoScrollKey?: unknown;
}) {
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const virtualizer = useVirtualizer({
    count: props.rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => props.estimateSize ?? 140,
    overscan: 6,
    getItemKey: (index) => props.rows[index]?.key ?? index,
  });
  const totalSize = virtualizer.getTotalSize();
  const virtualRows = virtualizer.getVirtualItems();

  useEffect(() => {
    if (props.rows.length === 0) return;
    virtualizer.scrollToIndex(props.rows.length - 1, { align: "end" });
  }, [props.autoScrollKey, props.rows.length, virtualizer]);

  return (
    <div ref={scrollRef} className="chat-scroll" data-testid="chat-message-list">
      <ol
        className="chat-messages"
        style={{ position: "relative", height: totalSize, display: "block", margin: 0, padding: 0, listStyle: "none" }}
      >
        {virtualRows.map((item) => (
          <li
            key={item.key}
            data-index={item.index}
            ref={virtualizer.measureElement}
            style={{
              position: "absolute",
              top: 0,
              left: 0,
              width: "100%",
              paddingBottom: 16,
              transform: `translateY(${item.start}px)`,
            }}
          >
            {props.renderRow(props.rows[item.index])}
          </li>
        ))}
      </ol>
    </div>
  );
}
