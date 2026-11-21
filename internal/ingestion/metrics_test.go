package ingestion

import (
	"bytes"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/observability"
)

func TestRecordParseMetrics(t *testing.T) {
	registry := observability.New()
	worker := &PipelineWorker{metrics: registry}
	worker.recordParseMetrics(SourceObject{MimeType: "text/csv"}, ParseResult{IndexedRows: 12, Truncated: true}, nil, 25*time.Millisecond)
	worker.recordParseMetrics(SourceObject{MimeType: "text/csv"}, ParseResult{}, nil, time.Millisecond)
	var out bytes.Buffer
	if err := registry.WritePrometheus(&out); err != nil {
		t.Fatalf("write metrics: %v", err)
	}
	text := out.String()
	for _, name := range []string{"ingestion_parse_total", "ingestion_parse_rows", "ingestion_parse_truncated_total"} {
		if !bytes.Contains(out.Bytes(), []byte(name)) {
			t.Fatalf("metric %s missing from:\n%s", name, text)
		}
	}
}
