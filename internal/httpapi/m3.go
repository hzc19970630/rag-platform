package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/conversation"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/retrieval"
	"github.com/gin-gonic/gin"
)

// RegisterM3Routes wires the authorized RAG surface: document preview and chunk
// queries, retrieval-only search, sessions, and the streaming answer endpoint.
func RegisterM3Routes(router *gin.Engine, cfg config.Config, application *app.Application, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, application))

	documents := protected.Group("/documents/:id")
	documents.GET("/preview", func(c *gin.Context) {
		source, err := application.Ingestion.SourceDocument(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		writeSourcePreview(c, source)
	})
	documents.GET("/content", func(c *gin.Context) {
		preview, err := application.Ingestion.Preview(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, preview)
	})
	documents.GET("/chunks", func(c *gin.Context) {
		page, err := application.Ingestion.Chunks(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "cursor"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
	})
	documents.GET("/status", func(c *gin.Context) {
		status, err := application.Ingestion.Status(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, status)
	})

	protected.GET("/chunks/:chunkID", func(c *gin.Context) {
		chunk, err := application.Ingestion.ChunkDetail(c.Request.Context(), accessContext(c).Principal, c.Param("chunkID"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, chunk)
	})

	protected.POST("/search", func(c *gin.Context) {
		var input struct {
			Query           string `json:"query"`
			KnowledgeBaseID string `json:"knowledgeBaseId"`
			SessionOverride string `json:"sessionOverride"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		access := accessContext(c)
		scope, err := application.Authorization.BuildScope(c.Request.Context(), access.Principal, authz.QueryTarget{KnowledgeBaseID: input.KnowledgeBaseID})
		if err != nil {
			writeError(c, err)
			return
		}
		evidence, err := application.Retrieval.Retrieve(c.Request.Context(), scope, retrieval.QueryInput{
			RequestID: requestIDFor(c), Query: input.Query, KnowledgeBaseID: input.KnowledgeBaseID, SessionOverride: input.SessionOverride,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, evidence)
	})

	sessions := protected.Group("/sessions")
	sessions.POST("", func(c *gin.Context) {
		var input conversation.CreateConversationInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := application.Conversation.CreateConversation(c.Request.Context(), accessContext(c).Principal, input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, row)
	})
	sessions.GET("", func(c *gin.Context) {
		rows, err := application.Conversation.ListConversations(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows})
	})
	sessions.GET("/:id/messages", func(c *gin.Context) {
		rows, err := application.Conversation.ListMessages(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows})
	})
	sessions.POST("/:id/messages", func(c *gin.Context) {
		var input struct {
			Query           string `json:"query"`
			RequestID       string `json:"requestId"`
			SessionOverride string `json:"sessionOverride"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		stream := newSSEStream(c)
		result, err := application.Conversation.Ask(c.Request.Context(), accessContext(c).Principal, conversation.AskInput{
			ConversationID: c.Param("id"), Query: input.Query, RequestID: requestIDOrDefault(c, input.RequestID), SessionOverride: input.SessionOverride,
		}, stream.emit)
		if err != nil {
			// Nothing was streamed yet, so a regular error response is still
			// possible; otherwise report the failure as a stream event.
			if !stream.started() {
				writeError(c, err)
				return
			}
			_ = stream.emit(conversation.Event{Type: conversation.EventError, Data: map[string]any{"code": "internal_error", "message": err.Error()}})
			return
		}
		_ = result
		logger.Info("answer_completed", "request_id", result.RequestID, "status", result.Status, "citations", len(result.Citations), "latency_ms", result.LatencyMS)
	})

	// Resume: replay the persisted stream events for a request so a client that
	// dropped the connection can continue from the last event it saw.
	protected.GET("/requests/:requestID/events", func(c *gin.Context) {
		after := intQuery(c, "after")
		if after == 0 {
			if lastEventID := strings.TrimSpace(c.GetHeader("Last-Event-ID")); lastEventID != "" {
				after = sequenceFromEventID(lastEventID)
			}
		}
		events, err := application.Conversation.ReplayEvents(c.Request.Context(), accessContext(c).Principal, c.Param("requestID"), after)
		if err != nil {
			writeError(c, err)
			return
		}
		stream := newSSEStream(c)
		for _, event := range events {
			if err := stream.emit(event); err != nil {
				return
			}
		}
		stream.finish()
	})

	_ = logger
}

// writeSourcePreview streams the original file without ever exposing the object
// storage location. Active content types are never served inline.
func writeSourcePreview(c *gin.Context, source ingestion.SourceDocument) {
	disposition := "attachment"
	switch source.MimeType {
	case "application/pdf", "text/markdown", "text/plain":
		disposition = "inline"
	}
	c.Header("Content-Type", source.MimeType)
	c.Header("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, sanitizeFilename(source.Filename)))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, no-store")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(source.Content)
}

func sanitizeFilename(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, name)
	if strings.TrimSpace(cleaned) == "" {
		return "document"
	}
	return cleaned
}

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
