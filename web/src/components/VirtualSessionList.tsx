import { useVirtualizer } from "@tanstack/react-virtual";
import { useRef, type ReactNode } from "react";

export type SessionRow = { key: string };

// Sessions are capped at 100 by the API, but the list is still grouped and
// scrollable; windowing keeps the sidebar cheap when the cap is reached.
export function VirtualSessionList<Row extends SessionRow>(props: {
  rows: Row[];
  renderRow: (row: Row) => ReactNode;
  estimateSize: (index: number) => number;
}) {
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const virtualizer = useVirtualizer({
    count: props.rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: props.estimateSize,
    overscan: 8,
    getItemKey: (index) => props.rows[index]?.key ?? index,
  });

  return (
    <div ref={scrollRef} className="chat-session-groups" data-testid="chat-session-list">
      <div style={{ position: "relative", height: virtualizer.getTotalSize() }}>
        {virtualizer.getVirtualItems().map((item) => (
          <div
            key={item.key}
            data-index={item.index}
            ref={virtualizer.measureElement}
            style={{
              position: "absolute",
              top: 0,
              left: 0,
              width: "100%",
              transform: `translateY(${item.start}px)`,
            }}
          >
            {props.renderRow(props.rows[item.index])}
          </div>
        ))}
      </div>
    </div>
  );
}
