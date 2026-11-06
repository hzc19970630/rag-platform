package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/example/rag-platform/internal/conversation"
	"github.com/example/rag-platform/internal/transport/views"
	"github.com/gin-gonic/gin"
)

// ssePingInterval is the idle-heartbeat cadence. Comment frames keep proxies
// from dropping an event stream that is still waiting for the model.
var ssePingInterval = 15 * time.Second

// sseStream lazily starts a text/event-stream response on the first event, so a
// pre-flight failure can still be returned as normal JSON.
type sseStream struct {
	c           *gin.Context
	mu          sync.Mutex
	startedFlag bool
	flusher     http.Flusher
	done        chan struct{}
	stopOnce    sync.Once
}

func newSSEStream(c *gin.Context) *sseStream {
	flusher, _ := c.Writer.(http.Flusher)
	return &sseStream{c: c, flusher: flusher}
}

func (s *sseStream) started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startedFlag
}

func (s *sseStream) startLocked() {
	if s.startedFlag {
		return
	}
	s.startedFlag = true
	s.c.Header("Content-Type", "text/event-stream")
	s.c.Header("Cache-Control", "no-cache")
	s.c.Header("Connection", "keep-alive")
	s.c.Header("X-Accel-Buffering", "no")
	s.c.Status(http.StatusOK)
	s.done = make(chan struct{})
	if s.flusher != nil {
		go s.heartbeat()
	}
}

// heartbeat writes a comment frame that EventSource and the web parser ignore.
// It exits when the handler finishes so an idle stream never outlives its
// request goroutine.
func (s *sseStream) heartbeat() {
	ticker := time.NewTicker(ssePingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		select {
		case <-s.done:
			s.mu.Unlock()
			return
		default:
		}
		_, err := fmt.Fprint(s.c.Writer, ": ping\n\n")
		if err == nil {
			s.flusher.Flush()
		}
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (s *sseStream) emit(event conversation.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startLocked()
	if event.EventID == "" {
		event.EventID = fmt.Sprintf("%s:%d", event.RequestID, event.Sequence)
	}
	payload, err := json.Marshal(views.StreamEvent(event))
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.c.Writer, "event: %s\nid: %s\ndata: %s\n\n", event.Type, event.EventID, payload); err != nil {
		return err
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return nil
}

func (s *sseStream) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startLocked()
	s.stopOnce.Do(func() {
		close(s.done)
	})
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// sequenceFromEventID parses the "<request>:<sequence>" event id format.
func sequenceFromEventID(value string) int {
	separator := strings.LastIndex(value, ":")
	if separator < 0 {
		return 0
	}
	sequence, err := strconv.Atoi(value[separator+1:])
	if err != nil || sequence < 0 {
		return 0
	}
	return sequence
}

func intQuery(c *gin.Context, name string) int {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

func requestIDFor(c *gin.Context) string {
	if value := strings.TrimSpace(c.GetHeader("X-Request-ID")); value != "" {
		return value
	}
	return ""
}

func requestIDOrDefault(c *gin.Context, provided string) string {
	if strings.TrimSpace(provided) != "" {
		return provided
	}
	return requestIDFor(c)
}
