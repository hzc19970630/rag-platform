package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/conversation"
	"github.com/example/rag-platform/internal/retrieval"
	"github.com/gin-gonic/gin"
)

func RegisterConversationRoutes(router *gin.Engine, cfg config.Config, application *app.Application, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, application))

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
}
