package httpapi

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/conversation"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

func RegisterConversationRoutes(protected *gin.RouterGroup, cfg config.Config, deps ConversationDeps, mw MiddlewareDeps, logger *slog.Logger) {
	protected.POST("/search", func(c *gin.Context) {
		var input httpapi.SearchRequest
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
		c.JSON(http.StatusOK, views.EvidenceSet(evidence))
	})

	sessions := protected.Group("/sessions")
	sessions.POST("", func(c *gin.Context) {
		var input httpapi.ConversationCreateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.Conversation.CreateConversation(c.Request.Context(), accessContext(c).Principal, views.CreateConversationInput(input))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, views.Conversation(row))
	})
	sessions.GET("", func(c *gin.Context) {
		rows, err := deps.Conversation.ListConversations(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.ConversationList{Items: views.Conversations(rows)})
	})
	sessions.GET("/:id/messages", func(c *gin.Context) {
		rows, err := deps.Conversation.ListMessages(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.MessageList{Items: views.MessageViews(rows)})
	})
	sessions.POST("/:id/messages", func(c *gin.Context) {
		var input httpapi.MessageCreateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		principal := accessContext(c).Principal
		requestID := requestIDOrDefault(c, input.RequestID)
		stream := newSSEStream(c)
		// A started stream must always stop its heartbeat, including the error
		// paths below. A pre-flight failure never starts it and stays plain JSON.
		defer func() {
			if stream.started() {
				stream.finish()
			}
		}()

		if deps.Conversation.AsyncEnabled() {
			started := time.Now()
			prepared, err := deps.Conversation.PrepareAnswer(c.Request.Context(), principal, views.AskInput(c.Param("id"), input, requestID))
			if err != nil {
				if !stream.started() {
					writeError(c, err)
					return
				}
				_ = stream.emit(conversation.Event{Type: conversation.EventError, Data: map[string]any{"code": "internal_error", "message": err.Error()}})
				return
			}
			// The pre-flight writes and the answer job must be durable before the
			// worker can claim them. In enforce mode the request transaction spans
			// the handler, so commit it before the long SSE tail; otherwise the
			// worker sees no job row until the tail gives up. The Existing replay
			// path has no new hand-off and keeps the request transaction.
			if prepared.Existing == nil {
				if err := tenantdb.CommitRequestTx(c.Request.Context()); err != nil {
					writeError(c, err)
					return
				}
			}
			if prepared.Existing != nil {
				events, replayErr := deps.Conversation.ReplayEvents(c.Request.Context(), principal, prepared.RequestID, 0)
				if replayErr != nil {
					if !stream.started() {
						writeError(c, replayErr)
						return
					}
					_ = stream.emit(conversation.Event{Type: conversation.EventError, Data: map[string]any{"code": "internal_error", "message": replayErr.Error()}})
					return
				}
				after := 0
				terminal := false
				for _, event := range events {
					if err := stream.emit(event); err != nil {
						return
					}
					after = event.Sequence
					if event.Type == conversation.EventComplete || event.Type == conversation.EventError {
						terminal = true
					}
				}
				if !terminal {
					streamRecordedAnswer(stream, *prepared.Existing, after)
				}
				logAnswerStream(logger, prepared.RequestID, started)
				return
			}
			tailAnswerStream(c.Request.Context(), deps.Conversation, principal, prepared.RequestID, 0, cfg, stream)
			logAnswerStream(logger, prepared.RequestID, started)
			return
		}

		result, err := deps.Conversation.Ask(c.Request.Context(), principal, views.AskInput(c.Param("id"), input, requestID), stream.emit)
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
	// dropped the connection can continue from the last event it saw. With
	// ?stream=1 the replay keeps polling until a terminal event, which is the
	// async connect/reconnect path; the default remains a one-shot replay.
	protected.GET("/requests/:requestID/events", func(c *gin.Context) {
		handleRequestEvents(c, deps, cfg)
	})
}

// handleRequestEvents is split out so the streaming transition is testable.
// A streaming tail can wait up to ANSWER_TAIL_TIMEOUT_SECONDS; committing the
// request transaction first keeps one pooled connection from being pinned for
// the whole stream. ReplayEvents opens a short tenant transaction per poll.
func handleRequestEvents(c *gin.Context, deps ConversationDeps, cfg config.Config) {
	after := intQuery(c, "after")
	if after == 0 {
		if lastEventID := strings.TrimSpace(c.GetHeader("Last-Event-ID")); lastEventID != "" {
			after = sequenceFromEventID(lastEventID)
		}
	}
	principal := accessContext(c).Principal
	if c.Query("stream") == "1" {
		if err := tenantdb.CommitRequestTx(c.Request.Context()); err != nil {
			writeError(c, err)
			return
		}
		stream := newSSEStream(c)
		defer stream.finish()
		tailAnswerStream(c.Request.Context(), deps.Conversation, principal, c.Param("requestID"), after, cfg, stream)
		return
	}
	events, err := deps.Conversation.ReplayEvents(c.Request.Context(), principal, c.Param("requestID"), after)
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
}
