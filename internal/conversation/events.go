package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"github.com/example/rag-platform/internal/retrieval"
	"gorm.io/gorm"
)

// events.go owns this slice of the service. Behavior and external contracts are
// unchanged; the split only narrows each file's responsibility.

// ReplayEvents returns persisted stream events after the given sequence so a
// client that dropped the connection can resume without re-running the answer.
func (s *Service) ReplayEvents(ctx context.Context, principal authz.Principal, requestID string, afterSequence int) ([]Event, error) {
	if strings.TrimSpace(requestID) == "" {
		return nil, apperror.Invalidf("request id is required")
	}
	var message models.Message
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("user_id = ? AND request_id = ?", principal.SubjectID, requestID).First(&message).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.ErrNotFound
		}
		return nil, fmt.Errorf("load request messages: %w", err)
	}
	var rows []models.StreamEvent
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("user_id = ? AND request_id = ? AND sequence > ?", principal.SubjectID, requestID, afterSequence).Order("sequence").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load stream events: %w", err)
	}
	events := make([]Event, 0, len(rows))
	for _, row := range rows {
		data := map[string]any{}
		_ = json.Unmarshal([]byte(row.Payload), &data)
		events = append(events, Event{EventID: eventID(row.RequestID, row.Sequence), RequestID: row.RequestID, Sequence: row.Sequence, Type: row.EventType, Data: data})
	}
	return events, nil
}

// PurgeStreamEvents deletes at most one batch of replay buffers older than the
// cutoff. Answers and citations live in messages/message_citations, so an
// expired request simply replays no events; it does not lose the answer.

// PurgeStreamEvents deletes at most one batch of replay buffers older than the
// cutoff. Answers and citations live in messages/message_citations, so an
// expired request simply replays no events; it does not lose the answer.
func (s *Service) PurgeStreamEvents(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() {
		return 0, nil
	}
	if limit <= 0 {
		limit = 1000
	}
	result := tenantdb.Unscoped(s.db.WithContext(ctx), "retention: purge expired stream events across tenants").Exec(`
		DELETE FROM stream_events
		WHERE id IN (
			SELECT id FROM stream_events WHERE created_at < ? ORDER BY created_at, id LIMIT ?
		)`, before, limit)
	if result.Error != nil {
		return 0, fmt.Errorf("purge stream events: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// Ask answers one question. Events are persisted before they are emitted, so a
// client that disconnects mid-answer can resume from the last sequence it saw.

func (s *Service) appendEvent(ctx context.Context, requestID, tenantID, userID string, sequence *int, eventType string, data map[string]any, emit Emitter) error {
	*sequence++
	return s.appendEventAt(ctx, requestID, tenantID, userID, *sequence, eventType, data, emit)
}

// appendEventAt persists one event at an explicit sequence. Async runs
// allocate the sequence from answer_jobs, so the API's pre-flight events and
// the worker's events share one counter even across processes.
func (s *Service) appendEventAt(ctx context.Context, requestID, tenantID, userID string, sequence int, eventType string, data map[string]any, emit Emitter) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal stream event: %w", err)
	}
	row := models.StreamEvent{RequestID: requestID, TenantID: tenantID, UserID: userID, Sequence: sequence, EventType: eventType, Payload: string(payload)}
	// Events are persisted before they are emitted so a reconnect can replay
	// them. The client may already be gone (ctx canceled); that must not stop
	// the terminal events from being recorded.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalPersistTimeout)
	defer cancel()
	if err := s.db.WithContext(writeCtx).Create(&row).Error; err != nil {
		return fmt.Errorf("persist stream event: %w", err)
	}
	if emit == nil {
		return nil
	}
	return emit(Event{EventID: eventID(requestID, sequence), RequestID: requestID, Sequence: sequence, Type: eventType, Data: data})
}

func eventID(requestID string, sequence int) string {
	return fmt.Sprintf("%s:%d", requestID, sequence)
}

func (s *Service) persistMessage(ctx context.Context, message models.Message) (string, error) {
	if message.ID == "" {
		id, err := newID("msg")
		if err != nil {
			return "", err
		}
		message.ID = id
	}
	if err := s.db.WithContext(ctx).Create(&message).Error; err != nil {
		return "", fmt.Errorf("persist message: %w", err)
	}
	return message.ID, nil
}

func (s *Service) persistCitations(ctx context.Context, messageID string, principal authz.Principal, conversation models.Conversation, citations []CitationView) error {
	for _, citation := range citations {
		row := models.MessageCitation{
			ID: stableID("cit", messageID, fmt.Sprint(citation.Index)), MessageID: messageID,
			TenantID: principal.TenantID, KnowledgeBaseID: conversation.KnowledgeBaseID,
			DocumentID: citation.DocumentID, DocumentVersionID: citation.DocumentVersionID,
			ChunkID: citation.ChunkID, CitationIndex: citation.Index, Rank: citation.Rank,
			Score: citation.Score, Quote: citation.Quote,
		}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return fmt.Errorf("persist citation: %w", err)
		}
	}
	return nil
}

func (s *Service) loadCitations(ctx context.Context, tenantID, messageID string) ([]CitationView, error) {
	var rows []models.MessageCitation
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Where("message_id = ?", messageID).Order("citation_index").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load citations: %w", err)
	}
	views := make([]CitationView, 0, len(rows))
	for _, row := range rows {
		views = append(views, CitationView{
			Index: row.CitationIndex, ChunkID: row.ChunkID, DocumentID: row.DocumentID,
			DocumentVersionID: row.DocumentVersionID, KnowledgeBaseID: row.KnowledgeBaseID,
			Quote: row.Quote, Rank: row.Rank, Score: row.Score,
		})
	}
	return views, nil
}

// loadAnswer returns a recorded assistant answer for an idempotent retry.
// Request ids are client-supplied, so the lookup must be scoped exactly like
// ReplayEvents: tenant + user + conversation, not the global request id alone.

// loadAnswer returns a recorded assistant answer for an idempotent retry.
// Request ids are client-supplied, so the lookup must be scoped exactly like
// ReplayEvents: tenant + user + conversation, not the global request id alone.
func (s *Service) loadAnswer(ctx context.Context, principal authz.Principal, conversationID, requestID string) (AnswerResult, bool, error) {
	var message models.Message
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("user_id = ? AND conversation_id = ? AND request_id = ? AND role = ?",
			principal.SubjectID, conversationID, requestID, MessageRoleAssistant).
		First(&message).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AnswerResult{}, false, nil
		}
		return AnswerResult{}, false, fmt.Errorf("load recorded answer: %w", err)
	}
	citations, err := s.loadCitations(ctx, principal.TenantID, message.ID)
	if err != nil {
		return AnswerResult{}, false, err
	}
	var validation Validation
	if message.CitationValidation != nil {
		_ = json.Unmarshal([]byte(*message.CitationValidation), &validation)
	}
	var evidence retrieval.EvidenceSet
	if message.EvidenceSet != nil {
		_ = json.Unmarshal([]byte(*message.EvidenceSet), &evidence)
	}
	var usage TokenUsage
	if message.TokenUsage != nil {
		_ = json.Unmarshal([]byte(*message.TokenUsage), &usage)
	}
	return AnswerResult{
		MessageID: message.ID, RequestID: message.RequestID, Status: message.Status, Content: message.Content,
		Citations: citations, Validation: validation, EvidenceSet: evidence, Model: message.Model,
		LatencyMS: message.LatencyMS, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		ErrorCode: message.ErrorCode, ErrorMessage: message.ErrorMessage,
	}, true, nil
}

const historyMessageMaxRunes = 2000

// loadHistory returns the most recent completed turns in chronological order.
// It reads before the current user message is persisted, so the current
// question never appears twice.
