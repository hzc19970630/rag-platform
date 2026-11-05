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
	"github.com/example/rag-platform/internal/contracts/metrics"
	"github.com/example/rag-platform/internal/contracts/model"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"github.com/example/rag-platform/internal/platform/token"
	"github.com/example/rag-platform/internal/prompts"
	"github.com/example/rag-platform/internal/retrieval"
)

// answer.go owns this slice of the service. Behavior and external contracts are
// unchanged; the split only narrows each file's responsibility.

// preparedAnswer is everything both execution paths need after the pre-flight
// checks: identity, authorization scope, conversation, model choice and
// history. The synchronous path keeps it in memory; the async path records the
// same values durably (scope in answer_jobs.scope_json, the rest in messages).
type preparedAnswer struct {
	conversation models.Conversation
	principal    authz.Principal
	scope        authz.AuthorizationScope
	requestID    string
	query        string
	// sessionOverride is the request-tier retrieval parameter payload. It is
	// configuration, not identity, so the async job may carry it.
	sessionOverride string
	history         []model.Turn
	chatModel       string
	answerModel     string
	started         time.Time
}

// prepare runs the pre-flight half of an answer. It returns an existing answer
// for an idempotent retry, so neither path spends a second model call.
func (s *Service) prepare(ctx context.Context, principal authz.Principal, input AskInput) (preparedAnswer, *AnswerResult, error) {
	started := time.Now()
	query := strings.Join(strings.Fields(strings.TrimSpace(input.Query)), " ")
	if query == "" {
		return preparedAnswer{}, nil, apperror.Invalidf("query is required")
	}
	conversation, err := s.loadConversation(ctx, principal, input.ConversationID)
	if err != nil {
		return preparedAnswer{}, nil, err
	}
	scope, err := s.authorizedKnowledgeBase(ctx, principal, conversation.KnowledgeBaseID)
	if err != nil {
		return preparedAnswer{}, nil, err
	}
	requestID := input.RequestID
	if strings.TrimSpace(requestID) == "" {
		generated, err := newID("req")
		if err != nil {
			return preparedAnswer{}, nil, err
		}
		requestID = generated
	}
	// Idempotent retry: a repeated request id returns the recorded answer rather
	// than generating and persisting a second one. The lookup is scoped to this
	// tenant, user and conversation so a leaked request id cannot disclose
	// another caller's answer.
	if existing, ok, err := s.loadAnswer(ctx, principal, conversation.ID, requestID); err != nil {
		return preparedAnswer{}, nil, err
	} else if ok {
		return preparedAnswer{requestID: requestID}, &existing, nil
	}
	if err := s.checkDailyQuota(ctx, principal.TenantID); err != nil {
		return preparedAnswer{}, nil, err
	}
	chatModel := ""
	if s.models != nil {
		resolved, err := s.models.ChatModel(ctx, principal.TenantID, conversation.KnowledgeBaseID)
		if err != nil {
			return preparedAnswer{}, nil, err
		}
		chatModel = resolved
	}
	// Record the effective model on every terminal path, not the adapter default:
	// a knowledge base may be bound to another model than the deployment one.
	answerModel := chatModel
	if strings.TrimSpace(answerModel) == "" {
		answerModel = s.chatName()
	}
	// Load history before persisting the current question so it contains only
	// prior turns. A history read failure is not worth losing the answer over,
	// so it degrades to a single-turn prompt.
	history, err := s.loadHistory(ctx, principal, conversation.ID)
	if err != nil {
		history = nil
	}
	return preparedAnswer{
		conversation: conversation, principal: principal, scope: scope,
		requestID: requestID, query: query, sessionOverride: input.SessionOverride, history: history,
		chatModel: chatModel, answerModel: answerModel, started: started,
	}, nil, nil
}

// Ask answers one question inside the HTTP request. Events are persisted before
// they are emitted, so a client that disconnects mid-answer can resume from the
// last sequence it saw. Async mode uses prepare/execute directly instead.
func (s *Service) Ask(ctx context.Context, principal authz.Principal, input AskInput, emit Emitter) (AnswerResult, error) {
	prepared, existing, err := s.prepare(ctx, principal, input)
	if err != nil {
		return AnswerResult{}, err
	}
	if existing != nil {
		return *existing, nil
	}

	sequence := 0
	emitEvent := func(eventType string, data map[string]any) error {
		return s.appendEvent(ctx, prepared.requestID, principal.TenantID, principal.SubjectID, &sequence, eventType, data, emit)
	}
	if _, err := s.persistMessage(ctx, models.Message{
		ConversationID: prepared.conversation.ID, TenantID: principal.TenantID, KnowledgeBaseID: prepared.conversation.KnowledgeBaseID,
		UserID: principal.SubjectID, RequestID: prepared.requestID, Role: MessageRoleUser, Content: prepared.query,
		Status: MessageStatusCompleted, AuthorizationScopeVersion: prepared.scope.ACLVersion,
	}); err != nil {
		return AnswerResult{}, err
	}
	if err := emitEvent(EventStatus, map[string]any{"state": AnswerReceived}); err != nil {
		return AnswerResult{}, err
	}
	if err := emitEvent(EventStatus, map[string]any{"state": AnswerAuthorized, "scope": prepared.scope.ACLVersion, "knowledgeBaseId": prepared.conversation.KnowledgeBaseID}); err != nil {
		return AnswerResult{}, err
	}
	if err := emitEvent(EventStatus, map[string]any{"state": AnswerPlanned, "intent": retrieval.IntentFact, "mode": retrieval.ModeEvidence}); err != nil {
		return AnswerResult{}, err
	}
	return s.execute(ctx, prepared, emitEvent)
}

// execute runs retrieval, generation and collapse into the terminal answer. It
// is shared by the synchronous request path and the async worker; the caller
// owns persistence of the user message and the first three stream events.
func (s *Service) execute(ctx context.Context, prepared preparedAnswer, emitEvent func(string, map[string]any) error) (AnswerResult, error) {
	conversation, principal, scope := prepared.conversation, prepared.principal, prepared.scope
	requestID, query, started := prepared.requestID, prepared.query, prepared.started
	history, chatModel, answerModel := prepared.history, prepared.chatModel, prepared.answerModel

	if err := emitEvent(EventStatus, map[string]any{"state": AnswerRetrieving}); err != nil {
		return AnswerResult{}, err
	}

	evidence, err := s.retrieval.Retrieve(ctx, scope, retrieval.QueryInput{
		RequestID: requestID, Query: query, KnowledgeBaseID: conversation.KnowledgeBaseID, SessionOverride: prepared.sessionOverride,
		History: retrievalHistory(history),
	})
	if err != nil {
		return s.failAnswer(ctx, emitEvent, conversation, principal, requestID, scope, query, started, answerModel, CodeModelError, err)
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

	if err := emitEvent(EventStatus, map[string]any{"state": AnswerGenerating, "model": answerModel}); err != nil {
		return AnswerResult{}, err
	}
	generateCtx, cancel := context.WithTimeout(ctx, s.timeout)
	response, generateErr := s.generate(generateCtx, query, registry, evidence, history, chatModel)
	cancel()
	if generateErr != nil {
		code := CodeModelError
		if errors.Is(generateErr, context.DeadlineExceeded) {
			code = CodeTimeout
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			code = CodeClientDisconnected
		}
		s.metrics.Inc("model_errors_total", metrics.Labels("model", s.chatName(), "stage", "generate", "code", code))
		validation := Validation{Valid: false, Reason: code, RegisteredIndexes: registry.Len()}
		result, finishErr := s.finishAnswer(ctx, emitEvent, finishInput{
			conversation: conversation, principal: principal, requestID: requestID, scope: scope,
			query: query, started: started, evidence: evidence, validation: validation,
			content: s.fallbackText(), status: statusForCode(code), code: code,
			model: answerModel, message: generateErr.Error(),
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
			model: answerModel, message: "citation validation rejected the generated answer",
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
			model: answerModel, message: "a cited chunk left the authorization scope before delivery",
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

func (s *Service) failAnswer(ctx context.Context, emitEvent func(string, map[string]any) error, conversation models.Conversation, principal authz.Principal, requestID string, scope authz.AuthorizationScope, query string, started time.Time, model string, code string, cause error) (AnswerResult, error) {
	evidence := retrieval.EvidenceSet{}
	validation := Validation{Valid: false, Reason: code}
	result, err := s.finishAnswer(ctx, emitEvent, finishInput{
		conversation: conversation, principal: principal, requestID: requestID, scope: scope,
		query: query, started: started, evidence: evidence, validation: validation,
		content: s.fallbackText(), status: AnswerFailed, code: code, model: model, message: cause.Error(),
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
func (s *Service) generate(ctx context.Context, query string, registry *Registry, evidence retrieval.EvidenceSet, history []model.Turn, chatModel string) (model.ChatResponse, error) {
	if s.chat == nil {
		return model.ChatResponse{}, fmt.Errorf("no generation adapter is configured")
	}
	items := make([]model.EvidenceItem, 0, registry.Len())
	for _, index := range registry.Indexes() {
		candidate, _ := registry.Lookup(index)
		items = append(items, model.EvidenceItem{Index: index, ChunkID: candidate.ChunkID, HeadingPath: candidate.HeadingPath, Text: candidate.Content})
	}
	systemInstruction, err := s.prompts.Render(prompts.SystemPrompt, nil)
	if err != nil {
		return model.ChatResponse{}, err
	}
	contextTemplate, ok := s.prompts.Raw(prompts.ContextTemplate)
	if !ok {
		return model.ChatResponse{}, fmt.Errorf("context template is not configured")
	}
	return s.chat.Complete(ctx, model.ChatRequest{SystemInstruction: systemInstruction, ContextTemplate: contextTemplate, Query: query, Evidence: items, History: history, Model: chatModel})
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
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.Message{}).
		Where("role = ? AND created_at >= ?", MessageRoleUser, start).
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
	response, err := s.chat.Complete(titleCtx, model.ChatRequest{ContextTemplate: template, Query: query, MaxTokens: 32})
	if err != nil || strings.TrimSpace(response.Text) == "" {
		return fallback
	}
	return truncateRunes(response.Text, conversationTitleMaxRunes)
}

// loadHistory returns the most recent completed turns in chronological order.
// It reads before the current user message is persisted, so the current
// question never appears twice.
func (s *Service) loadHistory(ctx context.Context, principal authz.Principal, conversationID string) ([]model.Turn, error) {
	if s.historyTurn <= 0 || s.historyMax <= 0 {
		return nil, nil
	}
	// Refused or failed answers are filtered out after loading, so fetch a few
	// extra rows to still fill the requested number of completed turns.
	limit := s.historyTurn*2 + 4
	var rows []models.Message
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("conversation_id = ? AND user_id = ?", conversationID, principal.SubjectID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load conversation history: %w", err)
	}
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
	return trimHistory(pairHistory(rows), s.historyMax), nil
}

// pairHistory keeps only complete turns: a user message followed by a
// completed assistant answer. A dangling question or a refusal is not replayed,
// because it would teach the model the wrong precedent.
func pairHistory(rows []models.Message) []model.Turn {
	turns := make([]model.Turn, 0, len(rows))
	var pending *models.Message
	for index := range rows {
		message := rows[index]
		switch message.Role {
		case MessageRoleUser:
			pending = &rows[index]
		case MessageRoleAssistant:
			if pending == nil {
				continue
			}
			if message.Status != MessageStatusCompleted {
				pending = nil
				continue
			}
			turns = append(turns,
				model.Turn{Role: MessageRoleUser, Content: truncateRunes(pending.Content, historyMessageMaxRunes)},
				model.Turn{Role: MessageRoleAssistant, Content: truncateRunes(message.Content, historyMessageMaxRunes)},
			)
			pending = nil
		}
	}
	return turns
}

// trimHistory keeps the newest complete turns that fit the history budget.
func trimHistory(turns []model.Turn, tokenLimit int) []model.Turn {
	if tokenLimit <= 0 || len(turns) == 0 {
		return nil
	}
	start := len(turns)
	used := 0
	for index := len(turns) - 2; index >= 0; index -= 2 {
		cost := token.Estimate(turns[index].Content) + token.Estimate(turns[index+1].Content)
		if used+cost > tokenLimit {
			break
		}
		used += cost
		start = index
	}
	return turns[start:]
}

func retrievalHistory(turns []model.Turn) []retrieval.HistoryTurn {
	if len(turns) == 0 {
		return nil
	}
	history := make([]retrieval.HistoryTurn, len(turns))
	for index, turn := range turns {
		history[index] = retrieval.HistoryTurn{Role: turn.Role, Content: turn.Content}
	}
	return history
}
