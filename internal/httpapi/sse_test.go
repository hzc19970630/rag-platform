package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/conversation"
	"github.com/gin-gonic/gin"
)

// lockedRecorder makes the heartbeat goroutine and the test reader safe to run
// concurrently.
type lockedRecorder struct {
	mu     sync.Mutex
	header http.Header
	body   bytes.Buffer
	status int
}

func newLockedRecorder() *lockedRecorder {
	return &lockedRecorder{header: make(http.Header)}
}

func (r *lockedRecorder) Header() http.Header { return r.header }

func (r *lockedRecorder) Write(content []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(content)
}

func (r *lockedRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
}

func (r *lockedRecorder) Flush() {}

func (r *lockedRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func TestSSEStreamSendsHeartbeatUntilFinish(t *testing.T) {
	previous := ssePingInterval
	ssePingInterval = 5 * time.Millisecond
	defer func() { ssePingInterval = previous }()

	gin.SetMode(gin.TestMode)
	recorder := newLockedRecorder()
	context, _ := gin.CreateTestContext(recorder)
	stream := newSSEStream(context)
	if err := stream.emit(conversation.Event{Type: "delta", RequestID: "req-1", Sequence: 1}); err != nil {
		t.Fatalf("emit event: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(recorder.String(), ": ping") {
		if time.Now().After(deadline) {
			t.Fatalf("no heartbeat frame arrived, body: %q", recorder.String())
		}
		time.Sleep(5 * time.Millisecond)
	}

	stream.finish()
	settled := recorder.String()
	time.Sleep(40 * time.Millisecond)
	if current := recorder.String(); current != settled {
		t.Fatalf("stream kept writing after finish: %q -> %q", settled, current)
	}
	if !strings.Contains(settled, "event: delta") {
		t.Fatalf("stream body lost the event frame: %q", settled)
	}
	if contentType := context.Writer.Header().Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", contentType)
	}
}

func TestSSEPreflightFailureStaysJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
	stream := newSSEStream(context)
	// Mirrors the conditional deferred finish in the POST handler: a stream that
	// never started must not force SSE headers over the JSON error response.
	if stream.started() {
		stream.finish()
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("content type = %q, want application/json", contentType)
	}
}
