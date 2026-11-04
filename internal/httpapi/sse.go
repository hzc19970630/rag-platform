package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/example/rag-platform/internal/conversation"
	"github.com/gin-gonic/gin"
)

// sseStream lazily starts a text/event-stream response on the first event, so a
// pre-flight failure can still be returned as normal JSON.
type sseStream struct {
	c           *gin.Context
	startedFlag bool
	flusher     http.Flusher
}

func newSSEStream(c *gin.Context) *sseStream {
	flusher, _ := c.Writer.(http.Flusher)
	return &sseStream{c: c, flusher: flusher}
}

func (s *sseStream) started() bool { return s.startedFlag }

func (s *sseStream) start() {
	if s.startedFlag {
		return
	}
	s.startedFlag = true
	s.c.Header("Content-Type", "text/event-stream")
	s.c.Header("Cache-Control", "no-cache")
	s.c.Header("Connection", "keep-alive")
	s.c.Header("X-Accel-Buffering", "no")
	s.c.Status(http.StatusOK)
}

func (s *sseStream) emit(event conversation.Event) error {
	s.start()
	if event.EventID == "" {
		event.EventID = fmt.Sprintf("%s:%d", event.RequestID, event.Sequence)
	}
	payload, err := json.Marshal(event)
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
	s.start()
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
