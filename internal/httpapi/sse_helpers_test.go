package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/internal/conversation"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

func newTestStream() (*sseStream, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	return newSSEStream(context), recorder
}

func TestStreamRecordedAnswerSynthesizesTerminalEvents(t *testing.T) {
	stream, recorder := newTestStream()
	result := conversation.AnswerResult{
		RequestID: "req-1", Content: "answer", Status: "answered", LatencyMS: 12,
		EvidenceSet: retrieval.EvidenceSet{Items: []retrieval.Candidate{{ChunkID: "chunk-1"}}},
	}
	streamRecordedAnswer(stream, result, 3)
	body := recorder.Body.String()
	if !strings.Contains(body, "event: answer") || !strings.Contains(body, "event: complete") {
		t.Fatalf("terminal events missing: %q", body)
	}
	if !strings.Contains(body, "chunk-1") || !strings.Contains(body, "req-1:4") || !strings.Contains(body, "req-1:5") {
		t.Fatalf("event payloads missing: %q", body)
	}
}

func TestLogAnswerStreamWritesSummary(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	logAnswerStream(logger, "req-1", time.Now().Add(-time.Second))
}

func TestSequenceFromEventID(t *testing.T) {
	cases := map[string]int{
		"req-1:42":     42,
		"req:with:7":   7,
		"no-separator": 0,
		"req:-1":       0,
		"req:not-int":  0,
		"":             0,
	}
	for value, want := range cases {
		if got := sequenceFromEventID(value); got != want {
			t.Fatalf("sequenceFromEventID(%q) = %d, want %d", value, got, want)
		}
	}
}

func TestRequestIDHelpers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	context.Request.Header.Set("X-Request-ID", "header-id")
	if got := requestIDFor(context); got != "header-id" {
		t.Fatalf("requestIDFor = %q", got)
	}
	if got := requestIDOrDefault(context, " provided "); got != " provided " {
		t.Fatalf("requestIDOrDefault(provided) = %q", got)
	}
	if got := requestIDOrDefault(context, "  "); got != "header-id" {
		t.Fatalf("requestIDOrDefault(header) = %q", got)
	}
	if got := intQuery(context, "missing"); got != 0 {
		t.Fatalf("intQuery missing = %d", got)
	}
}
