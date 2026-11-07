package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

// events.go owns this slice of the service. Behavior and external contracts are
// unchanged; the split only narrows each file's responsibility.

// ReplayEvents returns persisted stream events after the given sequence so a
// client that dropped the connection can resume without re-running the answer.
func (s *Service) ReplayEvents(ctx context.Context, principal authz.Principal, requestID string, afterSequence int) ([]Event, error) {
	if strings.TrimSpace(requestID) == "" {
		return nil, apperror.Invalidf("request id is required")
	}
	rows, err := s.events.Replay(ctx, principal.TenantID, principal.SubjectID, requestID, afterSequence)
	if err != nil {
		return nil, err
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
func (s *Service) PurgeStreamEvents(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() {
		return 0, nil
	}
	if limit <= 0 {
		limit = 1000
	}
	return s.events.Purge(ctx, before, limit)
}

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
	if err := s.events.Append(writeCtx, row); err != nil {
		return err
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
	if err := s.messages.Create(ctx, message); err != nil {
		return "", fmt.Errorf("persist message: %w", err)
	}
	return message.ID, nil
}

func (s *Service) persistCitations(ctx context.Context, messageID string, principal authz.Principal, conversation models.Conversation, citations []CitationView) error {
	rows := make([]models.MessageCitation, 0, len(citations))
	for _, citation := range citations {
		rows = append(rows, models.MessageCitation{
			ID: stableID("cit", messageID, fmt.Sprint(citation.Index)), MessageID: messageID,
			TenantID: principal.TenantID, KnowledgeBaseID: conversation.KnowledgeBaseID,
			DocumentID: citation.DocumentID, DocumentVersionID: citation.DocumentVersionID,
			ChunkID: citation.ChunkID, CitationIndex: citation.Index, Rank: citation.Rank,
			Score: citation.Score, Quote: citation.Quote,
		})
	}
	return s.messages.CreateCitations(ctx, rows)
}

func (s *Service) loadCitations(ctx context.Context, tenantID, messageID string) ([]CitationView, error) {
	rows, err := s.messages.Citations(ctx, tenantID, messageID)
	if err != nil {
		return nil, err
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
func (s *Service) loadAnswer(ctx context.Context, principal authz.Principal, conversationID, requestID string) (AnswerResult, bool, error) {
	message, ok, err := s.messages.FindAnswer(ctx, principal.TenantID, principal.SubjectID, conversationID, requestID)
	if err != nil || !ok {
		return AnswerResult{}, false, err
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
