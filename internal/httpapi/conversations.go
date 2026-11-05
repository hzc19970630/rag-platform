package httpapi

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/conversation"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/retrieval"
	"github.com/gin-gonic/gin"
)

func RegisterConversationRoutes(router *gin.Engine, cfg config.Config, deps ConversationDeps, mw MiddlewareDeps, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, mw))

	protected.POST("/search", func(c *gin.Context) {
		var input searchRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		access := accessContext(c)
		scope, err := deps.Authorization.BuildScope(c.Request.Context(), access.Principal, authz.QueryTarget{KnowledgeBaseID: input.KnowledgeBaseID})
		if err != nil {
			writeError(c, err)
			return
		}
		evidence, err := deps.Retrieval.Retrieve(c.Request.Context(), scope, retrieval.QueryInput{
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
		row, err := deps.Conversation.CreateConversation(c.Request.Context(), accessContext(c).Principal, input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, conversationView(row))
	})
	sessions.GET("", func(c *gin.Context) {
		rows, err := deps.Conversation.ListConversations(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		items := make([]conversationResponse, 0, len(rows))
		for _, row := range rows {
			items = append(items, conversationView(row))
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	sessions.GET("/:id/messages", func(c *gin.Context) {
		rows, err := deps.Conversation.ListMessages(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		items := make([]messageListView, 0, len(rows))
		for _, row := range rows {
			items = append(items, messageListView{Message: messageView(row.Message), Citations: row.Citations})
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	sessions.POST("/:id/messages", func(c *gin.Context) {
		var input messageCreateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		stream := newSSEStream(c)
		// A started stream must always stop its heartbeat, including the error
		// paths below. A pre-flight failure never starts it and stays plain JSON.
		defer func() {
			if stream.started() {
				stream.finish()
			}
		}()
		result, err := deps.Conversation.Ask(c.Request.Context(), accessContext(c).Principal, conversation.AskInput{
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
		events, err := deps.Conversation.ReplayEvents(c.Request.Context(), accessContext(c).Principal, c.Param("requestID"), after)
		if err != nil {
			writeError(c, err)
			return
		}
		stream := newSSEStream(c)
		defer stream.finish()
		for _, event := range events {
			if err := stream.emit(event); err != nil {
				return
			}
		}
	})
}

type searchRequest struct {
	Query           string `json:"query"`
	KnowledgeBaseID string `json:"knowledgeBaseId"`
	SessionOverride string `json:"sessionOverride"`
}

type messageCreateRequest struct {
	Query           string `json:"query" binding:"required"`
	RequestID       string `json:"requestId"`
	SessionOverride string `json:"sessionOverride"`
}

type conversationResponse struct {
	ID              string    `json:"id"`
	TenantID        string    `json:"tenantId"`
	KnowledgeBaseID string    `json:"knowledgeBaseId"`
	UserID          string    `json:"userId"`
	Title           string    `json:"title"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func conversationView(row models.Conversation) conversationResponse {
	return conversationResponse{
		ID: row.ID, TenantID: row.TenantID, KnowledgeBaseID: row.KnowledgeBaseID,
		UserID: row.UserID, Title: row.Title, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

type messageResponse struct {
	ID                        string    `json:"id"`
	ConversationID            string    `json:"conversationId"`
	TenantID                  string    `json:"tenantId"`
	KnowledgeBaseID           string    `json:"knowledgeBaseId"`
	UserID                    string    `json:"userId"`
	RequestID                 string    `json:"requestId"`
	Role                      string    `json:"role"`
	Content                   string    `json:"content"`
	Status                    string    `json:"status"`
	AuthorizationScopeVersion string    `json:"authorizationScopeVersion"`
	Model                     string    `json:"model"`
	LatencyMS                 int64     `json:"latencyMs"`
	ErrorCode                 string    `json:"errorCode,omitempty"`
	ErrorMessage              string    `json:"errorMessage,omitempty"`
	CreatedAt                 time.Time `json:"createdAt"`
	UpdatedAt                 time.Time `json:"updatedAt"`
}

func messageView(row models.Message) messageResponse {
	return messageResponse{
		ID: row.ID, ConversationID: row.ConversationID, TenantID: row.TenantID,
		KnowledgeBaseID: row.KnowledgeBaseID, UserID: row.UserID, RequestID: row.RequestID,
		Role: row.Role, Content: row.Content, Status: row.Status,
		AuthorizationScopeVersion: row.AuthorizationScopeVersion, Model: row.Model,
		LatencyMS: row.LatencyMS, ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

type messageListView struct {
	Message   messageResponse             `json:"message"`
	Citations []conversation.CitationView `json:"citations"`
}
