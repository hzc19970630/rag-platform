package conversation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/llm"
	"github.com/example/rag-platform/internal/platform/observability"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/prompts"
	"github.com/example/rag-platform/internal/retrieval"
	"gorm.io/gorm"
)

// localChatName identifies the offline generation adapter. It is used only to
// decide whether a model call is worth making for a conversation title.
const localChatName = "local-extractive"

// terminalPersistTimeout bounds the database writes that record a terminal
// answer and its stream events after the client is gone. The HTTP request
// context is already canceled in that case, so these writes must not inherit
// its cancellation; they still need a bound of their own.
const terminalPersistTimeout = 15 * time.Second

type Service struct {
	db        *gorm.DB
	authz     *authz.Service
	retrieval *retrieval.Service
	chat      llm.Chatter
	prompts   *prompts.Registry
	timeout   time.Duration
	dailyCap  int
}

type Options struct {
	DB        *gorm.DB
	Authz     *authz.Service
	Retrieval *retrieval.Service
	Chat      llm.Chatter
	Prompts   *prompts.Registry
	Timeout   time.Duration
	// ChatRequestsPerDay is the tenant-level daily question budget. Zero
	// disables the limit.
	ChatRequestsPerDay int
}

func NewService(options Options) *Service {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Service{
		db:        options.DB,
		authz:     options.Authz,
		retrieval: options.Retrieval,
		chat:      options.Chat,
		prompts:   options.Prompts,
		timeout:   timeout,
		dailyCap:  options.ChatRequestsPerDay,
	}
}

type CreateConversationInput struct {
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Title           string `json:"title"`
}

func (s *Service) CreateConversation(ctx context.Context, principal authz.Principal, input CreateConversationInput) (models.Conversation, error) {
	if strings.TrimSpace(input.KnowledgeBaseID) == "" {
		return models.Conversation{}, fmt.Errorf("knowledge_base_id is required")
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, input.KnowledgeBaseID); err != nil {
		return models.Conversation{}, err
	}
	id, err := newID("conv")
	if err != nil {
		return models.Conversation{}, err
	}
	conversation := models.Conversation{ID: id, TenantID: principal.TenantID, KnowledgeBaseID: input.KnowledgeBaseID, UserID: principal.SubjectID, Title: truncateRunes(input.Title, conversationTitleMaxRunes)}
	if err := s.db.WithContext(ctx).Create(&conversation).Error; err != nil {
		return models.Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return conversation, nil
}

func (s *Service) ListConversations(ctx context.Context, principal authz.Principal) ([]models.Conversation, error) {
	scope, err := s.authz.BuildScope(ctx, principal, authz.QueryTarget{})
	if err != nil {
		return nil, err
	}
	if len(scope.KnowledgeBaseIDs) == 0 {
		return []models.Conversation{}, nil
	}
	var rows []models.Conversation
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND knowledge_base_id IN ?", principal.TenantID, principal.SubjectID, scope.KnowledgeBaseIDs).Order("updated_at DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	return rows, nil
}

func (s *Service) ListMessages(ctx context.Context, principal authz.Principal, conversationID string) ([]MessageView, error) {
	conversation, err := s.loadConversation(ctx, principal, conversationID)
	if err != nil {
		return nil, err
	}
	var messages []models.Message
	if err := s.db.WithContext(ctx).Where("conversation_id = ?", conversation.ID).Order("created_at, id").Find(&messages).Error; err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	views := make([]MessageView, 0, len(messages))
	for _, message := range messages {
		citations, err := s.loadCitations(ctx, message.ID)
		if err != nil {
			return nil, err
		}
		views = append(views, MessageView{Message: message, Citations: citations})
	}
	return views, nil
}

// ReplayEvents returns persisted stream events after the given sequence so a
// client that dropped the connection can resume without re-running the answer.
func (s *Service) ReplayEvents(ctx context.Context, principal authz.Principal, requestID string, afterSequence int) ([]Event, error) {
	if strings.TrimSpace(requestID) == "" {
		return nil, fmt.Errorf("request id is required")
	}
	var message models.Message
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND request_id = ?", principal.TenantID, principal.SubjectID, requestID).First(&message).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.ErrNotFound
		}
		return nil, fmt.Errorf("load request messages: %w", err)
	}
	var rows []models.StreamEvent
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND request_id = ? AND sequence > ?", principal.TenantID, principal.SubjectID, requestID, afterSequence).Order("sequence").Find(&rows).Error; err != nil {
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
func (s *Service) PurgeStreamEvents(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() {
		return 0, nil
	}
	if limit <= 0 {
		limit = 1000
	}
	result := s.db.WithContext(ctx).Exec(`
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
func (s *Service) Ask(ctx context.Context, principal authz.Principal, input AskInput, emit Emitter) (AnswerResult, error) {
	started := time.Now()
	query := strings.Join(strings.Fields(strings.TrimSpace(input.Query)), " ")
	if query == "" {
		return AnswerResult{}, fmt.Errorf("query is required")
	}
	conversation, err := s.loadConversation(ctx, principal, input.ConversationID)
	if err != nil {
		return AnswerResult{}, err
	}
	scope, err := s.authorizedKnowledgeBase(ctx, principal, conversation.KnowledgeBaseID)
	if err != nil {
		return AnswerResult{}, err
	}
	requestID := input.RequestID
	if strings.TrimSpace(requestID) == "" {
		generated, err := newID("req")
		if err != nil {
			return AnswerResult{}, err
		}
		requestID = generated
	}
	// Idempotent retry: a repeated request id returns the recorded answer rather
	// than generating and persisting a second one. The lookup is scoped to this
	// tenant, user and conversation so a leaked request id cannot disclose
	// another caller's answer.
	if existing, ok, err := s.loadAnswer(ctx, principal, conversation.ID, requestID); err != nil {
		return AnswerResult{}, err
	} else if ok {
		return existing, nil
	}
	if err := s.checkDailyQuota(ctx, principal.TenantID); err != nil {
		return AnswerResult{}, err
	}

	sequence := 0
	emitEvent := func(eventType string, data map[string]any) error {
		return s.appendEvent(ctx, requestID, principal.TenantID, principal.SubjectID, &sequence, eventType, data, emit)
	}
	if _, err := s.persistMessage(ctx, models.Message{
		ConversationID: conversation.ID, TenantID: principal.TenantID, KnowledgeBaseID: conversation.KnowledgeBaseID,
		UserID: principal.SubjectID, RequestID: requestID, Role: MessageRoleUser, Content: query,
		Status: MessageStatusCompleted, AuthorizationScopeVersion: scope.ACLVersion,
	}); err != nil {
		return AnswerResult{}, err
	}
	if err := emitEvent(EventStatus, map[string]any{"state": AnswerReceived}); err != nil {
		return AnswerResult{}, err
	}
	if err := emitEvent(EventStatus, map[string]any{"state": AnswerAuthorized, "scope": scope.ACLVersion, "knowledgeBaseId": conversation.KnowledgeBaseID}); err != nil {
		return AnswerResult{}, err
	}
	if err := emitEvent(EventStatus, map[string]any{"state": AnswerPlanned, "intent": retrieval.IntentFact, "mode": retrieval.ModeEvidence}); err != nil {
		return AnswerResult{}, err
	}
	if err := emitEvent(EventStatus, map[string]any{"state": AnswerRetrieving}); err != nil {
		return AnswerResult{}, err
	}

	evidence, err := s.retrieval.Retrieve(ctx, scope, retrieval.QueryInput{
		RequestID: requestID, Query: query, KnowledgeBaseID: conversation.KnowledgeBaseID, SessionOverride: input.SessionOverride,
	})
	if err != nil {
		return s.failAnswer(ctx, emitEvent, conversation, principal, requestID, scope, query, started, CodeModelError, err)
	}
	registry := NewRegistry(evidence)
	references := make([]CitationView, 0, len(evidence.Items))
	for _, index := range registry.Indexes() {
		candidate, _ := registry.Lookup(index)
		references = append(references, citationView(index, candidate))
	}
	if err := emitEvent(EventReferences, map[string]any{"citations": references, "tokenEstimate": evidence.TokenEstimate}); err != nil {
		return AnswerResult{}, err
	}
	if err := emitEvent(EventStatus, map[string]any{"state": AnswerGrounded, "evidenceCount": len(evidence.Items), "rerankApplied": evidence.RerankApplied}); err != nil {
		return AnswerResult{}, err
	}

	// Insufficient evidence: refuse rather than let the model fill the gap from
	// its own knowledge.
	if !evidence.Relevance() {
		validation := Validation{Valid: false, Reason: CodeInsufficientEvidence, RegisteredIndexes: 0}
		return s.finishAnswer(ctx, emitEvent, finishInput{
			conversation: conversation, principal: principal, requestID: requestID, scope: scope,
			query: query, started: started, evidence: evidence, validation: validation,
			content: s.fallbackText(), status: AnswerRefused, code: CodeInsufficientEvidence,
			model: s.chatName(), message: "no authorized evidence matched the question",
		})
	}

	if err := emitEvent(EventStatus, map[string]any{"state": AnswerGenerating, "model": s.chatName()}); err != nil {
		return AnswerResult{}, err
	}
	generateCtx, cancel := context.WithTimeout(ctx, s.timeout)
	response, generateErr := s.generate(generateCtx, query, registry, evidence)
	cancel()
	if generateErr != nil {
		code := CodeModelError
		if errors.Is(generateErr, context.DeadlineExceeded) {
			code = CodeTimeout
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			code = CodeClientDisconnected
		}
		observability.Default().Inc("model_errors_total", observability.Labels("model", s.chatName(), "stage", "generate", "code", code))
		validation := Validation{Valid: false, Reason: code, RegisteredIndexes: registry.Len()}
		result, finishErr := s.finishAnswer(ctx, emitEvent, finishInput{
			conversation: conversation, principal: principal, requestID: requestID, scope: scope,
			query: query, started: started, evidence: evidence, validation: validation,
			content: s.fallbackText(), status: statusForCode(code), code: code,
			model: s.chatName(), message: generateErr.Error(),
		})
		if finishErr != nil {
			return AnswerResult{}, finishErr
		}
		return result, nil
	}
	if err := emitEvent(EventStatus, map[string]any{"state": AnswerCitationValidating}); err != nil {
		return AnswerResult{}, err
	}

	validation := Validate(response.Text, evidence)
	if !validation.Valid {
		// An answer with an unregistered, unauthorized or missing citation is
		// rejected: the invalid text is never delivered.
		_ = emitEvent(EventThinking, map[string]any{"reason": validation.Reason, "invalidIndexes": validation.InvalidIndexes, "ungrounded": validation.Ungrounded})
		if validation.InjectionSuspected {
			_ = emitEvent(EventThinking, map[string]any{"injectionSuspected": true})
		}
		return s.finishAnswer(ctx, emitEvent, finishInput{
			conversation: conversation, principal: principal, requestID: requestID, scope: scope,
			query: query, started: started, evidence: evidence, validation: validation,
			content: s.fallbackText(), status: AnswerRefused, code: validation.Reason,
			model: s.chatName(), message: "citation validation rejected the generated answer",
			promptTokens: response.PromptTokens, completionTokens: response.CompletionTokens,
		})
	}

	citations, dropped, err := s.resolveCitations(ctx, scope, validation.CitedIndexes, registry)
	if err != nil {
		return AnswerResult{}, err
	}
	if dropped > 0 {
		validation.Valid = false
		validation.Reason = CodeCitationRejected
		_ = emitEvent(EventThinking, map[string]any{"reason": CodeCitationRejected, "droppedCitations": dropped})
		return s.finishAnswer(ctx, emitEvent, finishInput{
			conversation: conversation, principal: principal, requestID: requestID, scope: scope,
			query: query, started: started, evidence: evidence, validation: validation,
			content: s.fallbackText(), status: AnswerRefused, code: CodeCitationRejected,
			model: s.chatName(), message: "a cited chunk left the authorization scope before delivery",
			promptTokens: response.PromptTokens, completionTokens: response.CompletionTokens,
		})
	}
	if err := emitEvent(EventStatus, map[string]any{"state": AnswerDelivering, "citationCount": len(citations)}); err != nil {
		return AnswerResult{}, err
	}
	// The validated answer keeps its citation markers: they are all registered
	// ids, and the client renders them as links into the chunk preview.
	return s.finishAnswer(ctx, emitEvent, finishInput{
		conversation: conversation, principal: principal, requestID: requestID, scope: scope,
		query: query, started: started, evidence: evidence, validation: validation,
		content: response.Text, status: AnswerCompleted,
		model: response.Model, citations: citations,
		promptTokens: response.PromptTokens, completionTokens: response.CompletionTokens,
	})
}

type finishInput struct {
	conversation     models.Conversation
	principal        authz.Principal
	requestID        string
	scope            authz.AuthorizationScope
	query            string
	started          time.Time
	evidence         retrieval.EvidenceSet
	validation       Validation
	content          string
	status           string
	code             string
	model            string
	message          string
	citations        []CitationView
	promptTokens     int
	completionTokens int
}

// finishAnswer persists the assistant message and its citations, then emits the
// terminal events.
func (s *Service) finishAnswer(ctx context.Context, emitEvent func(string, map[string]any) error, input finishInput) (AnswerResult, error) {
	latency := time.Since(input.started).Milliseconds()
	planJSON, _ := json.Marshal(input.evidence.Plan)
	evidenceJSON, _ := input.evidence.Encode()
	validationJSON, _ := json.Marshal(input.validation)
	usageJSON, _ := json.Marshal(TokenUsage{PromptTokens: input.promptTokens, CompletionTokens: input.completionTokens})

	// A dropped SSE client cancels ctx, but the terminal answer still has to be
	// durable: /requests/:id/events replays the message and its events after a
	// reconnect. Only the optional conversation title stays on the request
	// context, so a disconnect does not spend a model call on it.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalPersistTimeout)
	defer cancel()

	assistant := models.Message{
		ConversationID: input.conversation.ID, TenantID: input.principal.TenantID, KnowledgeBaseID: input.conversation.KnowledgeBaseID,
		UserID: input.principal.SubjectID, RequestID: input.requestID, Role: MessageRoleAssistant,
		Content: input.content, Status: input.status, QueryPlan: jsonField(string(planJSON)), EvidenceSet: jsonField(evidenceJSON),
		CitationValidation: jsonField(string(validationJSON)), AuthorizationScopeVersion: input.scope.ACLVersion,
		Model: input.model, TokenUsage: jsonField(string(usageJSON)), LatencyMS: latency,
		ErrorCode: input.code, ErrorMessage: input.message,
	}
	messageID, err := s.persistMessage(writeCtx, assistant)
	if err != nil {
		return AnswerResult{}, err
	}
	if err := s.persistCitations(writeCtx, messageID, input.principal, input.conversation, input.citations); err != nil {
		return AnswerResult{}, err
	}
	if err := s.touchConversation(ctx, input.conversation, input.query); err != nil && ctx.Err() == nil {
		return AnswerResult{}, err
	}
	// Emit an allocated list so a refusal is "citations": [] rather than null.
	citations := input.citations
	if citations == nil {
		citations = []CitationView{}
	}
	if err := emitEvent(EventAnswer, map[string]any{
		"content": input.content, "citations": citations, "status": input.status,
		"validation": input.validation, "evidenceChunkIds": evidenceChunkIDs(input.evidence),
	}); err != nil {
		return AnswerResult{}, err
	}
	if err := emitEvent(EventComplete, map[string]any{"status": input.status, "code": input.code, "latencyMs": latency}); err != nil {
		return AnswerResult{}, err
	}
	return AnswerResult{
		MessageID: messageID, RequestID: input.requestID, Status: input.status, Content: input.content,
		Citations: input.citations, Validation: input.validation, EvidenceSet: input.evidence,
		Model: input.model, LatencyMS: latency, PromptTokens: input.promptTokens, CompletionTokens: input.completionTokens,
		ErrorCode: input.code, ErrorMessage: input.message,
	}, nil
}

func (s *Service) failAnswer(ctx context.Context, emitEvent func(string, map[string]any) error, conversation models.Conversation, principal authz.Principal, requestID string, scope authz.AuthorizationScope, query string, started time.Time, code string, cause error) (AnswerResult, error) {
	evidence := retrieval.EvidenceSet{}
	validation := Validation{Valid: false, Reason: code}
	result, err := s.finishAnswer(ctx, emitEvent, finishInput{
		conversation: conversation, principal: principal, requestID: requestID, scope: scope,
		query: query, started: started, evidence: evidence, validation: validation,
		content: s.fallbackText(), status: AnswerFailed, code: code, model: s.chatName(), message: cause.Error(),
	})
	if err != nil {
		return AnswerResult{}, err
	}
	return result, nil
}

func statusForCode(code string) string {
	switch code {
	case CodeTimeout:
		return AnswerTimedOut
	case CodeClientDisconnected:
		return AnswerCancelled
	case CodeCancelled:
		return AnswerCancelled
	default:
		return AnswerFailed
	}
}

// generate calls the generation adapter with the registered evidence only.
func (s *Service) generate(ctx context.Context, query string, registry *Registry, evidence retrieval.EvidenceSet) (llm.ChatResponse, error) {
	if s.chat == nil {
		return llm.ChatResponse{}, fmt.Errorf("no generation adapter is configured")
	}
	items := make([]llm.EvidenceItem, 0, registry.Len())
	for _, index := range registry.Indexes() {
		candidate, _ := registry.Lookup(index)
		items = append(items, llm.EvidenceItem{Index: index, ChunkID: candidate.ChunkID, HeadingPath: candidate.HeadingPath, Text: candidate.Content})
	}
	systemInstruction, err := s.prompts.Render(prompts.SystemPrompt, nil)
	if err != nil {
		return llm.ChatResponse{}, err
	}
	contextTemplate, ok := s.prompts.Raw(prompts.ContextTemplate)
	if !ok {
		return llm.ChatResponse{}, fmt.Errorf("context template is not configured")
	}
	return s.chat.Complete(ctx, llm.ChatRequest{SystemInstruction: systemInstruction, ContextTemplate: contextTemplate, Query: query, Evidence: items})
}

// resolveCitations expands cited indexes into full citations and re-checks each
// one against the current authorization scope. It returns how many were dropped.
func (s *Service) resolveCitations(ctx context.Context, scope authz.AuthorizationScope, indexes []int, registry *Registry) ([]CitationView, int, error) {
	if len(indexes) == 0 {
		return nil, 0, nil
	}
	refs := make([]authz.ResourceRef, 0, len(indexes))
	views := make([]CitationView, 0, len(indexes))
	for rank, index := range indexes {
		candidate, ok := registry.Lookup(index)
		if !ok {
			return nil, 0, apperror.ErrForbidden
		}
		refs = append(refs, authz.ResourceRef{TenantID: scope.TenantID, KnowledgeBaseID: candidate.KnowledgeBaseID, ResourceID: candidate.ChunkID, ResourceType: "chunk"})
		citation := citationView(index, candidate)
		citation.Rank = rank + 1
		views = append(views, citation)
	}
	readable, err := s.authz.FilterReadable(ctx, scope, refs)
	if err != nil {
		return nil, 0, err
	}
	allowed := map[string]bool{}
	for _, ref := range readable {
		allowed[ref.ResourceID] = true
	}
	kept := make([]CitationView, 0, len(views))
	for _, view := range views {
		if allowed[view.ChunkID] {
			kept = append(kept, view)
		}
	}
	return kept, len(views) - len(kept), nil
}

const citationQuoteMaxRunes = 200

// evidenceChunkIDs lists the chunk ids of the authorized evidence set. It is
// emitted with the answer so a client (or the evaluation harness) can verify
// that every citation came from this run's evidence.
func evidenceChunkIDs(evidence retrieval.EvidenceSet) []string {
	ids := make([]string, 0, len(evidence.Items))
	for _, item := range evidence.Items {
		ids = append(ids, item.ChunkID)
	}
	return ids
}

// jsonField keeps an absent payload as SQL NULL. An empty string is not a valid
// jsonb literal, so user messages must not write "" into these columns.
func jsonField(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func citationView(index int, candidate retrieval.Candidate) CitationView {
	return CitationView{
		Index: index, ChunkID: candidate.ChunkID, DocumentID: candidate.DocumentID,
		DocumentVersionID: candidate.DocumentVersionID, KnowledgeBaseID: candidate.KnowledgeBaseID,
		HeadingPath: candidate.HeadingPath, PageNumber: candidate.PageNumber, SourcePath: candidate.SourcePath,
		Quote: truncateRunes(candidate.Content, citationQuoteMaxRunes), Score: candidate.Score,
	}
}

// checkDailyQuota counts persisted user questions, not HTTP requests: a retry
// with the same request id returns the recorded answer before this point, so it
// does not consume budget twice.
func (s *Service) checkDailyQuota(ctx context.Context, tenantID string) error {
	if s.dailyCap <= 0 {
		return nil
	}
	start := time.Now().UTC().Truncate(24 * time.Hour)
	var count int64
	if err := s.db.WithContext(ctx).Model(&models.Message{}).
		Where("tenant_id = ? AND role = ? AND created_at >= ?", tenantID, MessageRoleUser, start).
		Count(&count).Error; err != nil {
		return fmt.Errorf("check daily chat quota: %w", err)
	}
	if count >= int64(s.dailyCap) {
		return fmt.Errorf("%w: the daily chat budget of %d questions has been consumed", apperror.ErrQuotaExceeded, s.dailyCap)
	}
	return nil
}

func (s *Service) fallbackText() string {
	text, err := s.prompts.Render(prompts.Fallback, nil)
	if err != nil || strings.TrimSpace(text) == "" {
		return "insufficient evidence"
	}
	return text
}

func (s *Service) chatName() string {
	if s.chat == nil {
		return ""
	}
	return s.chat.Name()
}

// titleFor derives a conversation title. A model-backed title is only requested
// from a real generation adapter; the local adapter uses a deterministic
// truncation so development runs stay offline and repeatable.
func (s *Service) titleFor(ctx context.Context, query string) string {
	fallback := truncateRunes(query, conversationTitleMaxRunes)
	if s.chat == nil || s.prompts == nil || s.chat.Name() == localChatName {
		return fallback
	}
	template, ok := s.prompts.Raw(prompts.ConversationTitle)
	if !ok {
		return fallback
	}
	titleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := s.chat.Complete(titleCtx, llm.ChatRequest{ContextTemplate: template, Query: query, MaxTokens: 32})
	if err != nil || strings.TrimSpace(response.Text) == "" {
		return fallback
	}
	return truncateRunes(response.Text, conversationTitleMaxRunes)
}

func (s *Service) touchConversation(ctx context.Context, conversation models.Conversation, query string) error {
	values := map[string]any{"updated_at": time.Now()}
	if strings.TrimSpace(conversation.Title) == "" {
		values["title"] = s.titleFor(ctx, query)
	}
	return s.db.WithContext(ctx).Model(&models.Conversation{}).Where("id = ?", conversation.ID).Updates(values).Error
}

func (s *Service) loadConversation(ctx context.Context, principal authz.Principal, conversationID string) (models.Conversation, error) {
	if strings.TrimSpace(conversationID) == "" {
		return models.Conversation{}, apperror.ErrNotFound
	}
	var conversation models.Conversation
	if err := s.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ? AND id = ?", principal.TenantID, principal.SubjectID, conversationID).First(&conversation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Conversation{}, apperror.ErrNotFound
		}
		return models.Conversation{}, fmt.Errorf("load conversation: %w", err)
	}
	return conversation, nil
}

func (s *Service) authorizedKnowledgeBase(ctx context.Context, principal authz.Principal, knowledgeBaseID string) (authz.AuthorizationScope, error) {
	scope, err := s.authz.BuildScope(ctx, principal, authz.QueryTarget{KnowledgeBaseID: knowledgeBaseID})
	if err != nil {
		return authz.AuthorizationScope{}, err
	}
	allowed, err := s.authz.Authorize(ctx, principal, "read", "/knowledge-bases/"+knowledgeBaseID)
	if err != nil {
		return authz.AuthorizationScope{}, err
	}
	if !allowed {
		return authz.AuthorizationScope{}, apperror.ErrForbidden
	}
	return scope, nil
}

func (s *Service) appendEvent(ctx context.Context, requestID, tenantID, userID string, sequence *int, eventType string, data map[string]any, emit Emitter) error {
	*sequence++
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal stream event: %w", err)
	}
	row := models.StreamEvent{RequestID: requestID, TenantID: tenantID, UserID: userID, Sequence: *sequence, EventType: eventType, Payload: string(payload)}
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
	return emit(Event{EventID: eventID(requestID, *sequence), RequestID: requestID, Sequence: *sequence, Type: eventType, Data: data})
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

func (s *Service) loadCitations(ctx context.Context, messageID string) ([]CitationView, error) {
	var rows []models.MessageCitation
	if err := s.db.WithContext(ctx).Where("message_id = ?", messageID).Order("citation_index").Find(&rows).Error; err != nil {
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
func (s *Service) loadAnswer(ctx context.Context, principal authz.Principal, conversationID, requestID string) (AnswerResult, bool, error) {
	var message models.Message
	if err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND conversation_id = ? AND request_id = ? AND role = ?",
			principal.TenantID, principal.SubjectID, conversationID, requestID, MessageRoleAssistant).
		First(&message).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AnswerResult{}, false, nil
		}
		return AnswerResult{}, false, fmt.Errorf("load recorded answer: %w", err)
	}
	citations, err := s.loadCitations(ctx, message.ID)
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

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func stableID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + "-" + hex.EncodeToString(digest[:])[:32]
}

func newID(prefix string) (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "-" + hex.EncodeToString(buffer), nil
}
