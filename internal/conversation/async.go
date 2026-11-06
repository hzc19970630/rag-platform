package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/contracts/tasks"
	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/retrieval"
)

// Async answer job states. They describe durable hand-off, not the answer:
// a job is COMPLETED once a terminal assistant message exists, even when that
// message is a refusal or a timeout. FAILED means no terminal answer was
// produced (lost worker, denied scope, queue unavailable).
const (
	AnswerJobQueued    = "QUEUED"
	AnswerJobRunning   = "RUNNING"
	AnswerJobCompleted = "COMPLETED"
	AnswerJobFailed    = "FAILED"
)

// errAnswerJobNotVisible is returned when a worker consumes the Asynq task
// before the API request transaction commits the answer_jobs row. Enforce mode
// wraps the whole request in one transaction, so the publish happens first;
// returning an error lets Asynq retry instead of silently acking the task.
var errAnswerJobNotVisible = errors.New("answer job is not visible yet")

// PreparedAnswer is the API-side result of accepting one question. Existing is
// set for an idempotent retry; the caller should replay the recorded answer
// instead of waiting for a new terminal event.
type PreparedAnswer struct {
	RequestID string
	Existing  *AnswerResult
}

// AsyncEnabled reports whether POST /sessions/:id/messages should enqueue work.
func (s *Service) AsyncEnabled() bool { return s.async }

// PrepareAnswer performs the pre-flight checks, records the question and the
// first three stream events, and enqueues exactly one generation job. The user
// message and events are written only by the request that creates the job, so a
// concurrent retry cannot duplicate them.
func (s *Service) PrepareAnswer(ctx context.Context, principal authz.Principal, input AskInput) (PreparedAnswer, error) {
	if s.answers == nil {
		return PreparedAnswer{}, fmt.Errorf("async answer publisher is not configured")
	}
	prepared, existing, err := s.prepare(ctx, principal, input)
	if err != nil {
		return PreparedAnswer{}, err
	}
	if existing != nil {
		return PreparedAnswer{RequestID: prepared.requestID, Existing: existing}, nil
	}
	job, created, err := s.ensureAnswerJob(ctx, prepared)
	if err != nil {
		return PreparedAnswer{}, err
	}
	if !created {
		// The answer may have landed between the pre-flight lookup and the
		// insert; otherwise an active job is already generating it.
		if recorded, ok, loadErr := s.loadAnswer(ctx, principal, prepared.conversation.ID, prepared.requestID); loadErr != nil {
			return PreparedAnswer{}, loadErr
		} else if ok {
			return PreparedAnswer{RequestID: prepared.requestID, Existing: &recorded}, nil
		}
		if job.Status != AnswerJobFailed {
			return PreparedAnswer{RequestID: prepared.requestID}, nil
		}
		if err := s.requeueAnswerJob(ctx, &job); err != nil {
			return PreparedAnswer{}, err
		}
	} else {
		if _, err := s.persistMessage(ctx, models.Message{
			ConversationID: prepared.conversation.ID, TenantID: principal.TenantID, KnowledgeBaseID: prepared.conversation.KnowledgeBaseID,
			UserID: principal.SubjectID, RequestID: prepared.requestID, Role: MessageRoleUser, Content: prepared.query,
			Status: MessageStatusCompleted, AuthorizationScopeVersion: prepared.scope.ACLVersion,
		}); err != nil {
			return PreparedAnswer{}, s.abortAnswerJob(ctx, &job, CodeQueueUnavailable, err)
		}
		emit := s.jobEvents(ctx, &job, nil)
		for _, event := range []struct {
			eventType string
			data      map[string]any
		}{
			{EventStatus, map[string]any{"state": AnswerReceived}},
			{EventStatus, map[string]any{"state": AnswerAuthorized, "scope": prepared.scope.ACLVersion, "knowledgeBaseId": prepared.conversation.KnowledgeBaseID}},
			{EventStatus, map[string]any{"state": AnswerPlanned, "intent": retrieval.IntentFact, "mode": retrieval.ModeEvidence}},
		} {
			if err := emit(event.eventType, event.data); err != nil {
				return PreparedAnswer{}, s.abortAnswerJob(ctx, &job, CodeQueueUnavailable, err)
			}
		}
	}
	timeout := int(s.timeout / time.Second)
	if err := s.answers.PublishAnswer(ctx, tasks.Answer{
		SchemaVersion: tasks.SchemaAnswer, JobID: job.ID, TenantID: job.TenantID, SubjectID: job.UserID,
		ConversationID: job.ConversationID, RequestID: job.RequestID,
		SessionOverride: prepared.sessionOverride, TimeoutSeconds: timeout,
	}); err != nil {
		return PreparedAnswer{}, s.abortAnswerJob(ctx, &job, CodeQueueUnavailable, fmt.Errorf("enqueue answer job: %w", err))
	}
	return PreparedAnswer{RequestID: job.RequestID}, nil
}

// RunAnswer executes one queued answer job. It is safe to call more than once:
// the conditional claim admits one worker at a time, and a recorded answer is
// returned without a second model call.
func (s *Service) RunAnswer(ctx context.Context, task tasks.Answer) error {
	if strings.TrimSpace(task.JobID) == "" || strings.TrimSpace(task.TenantID) == "" {
		return fmt.Errorf("answer task is missing its job identity")
	}
	job, claimed, err := s.claimAnswerJob(ctx, task)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	principal := authz.Principal{SubjectID: task.SubjectID, TenantID: task.TenantID}
	if recorded, ok, err := s.loadAnswer(ctx, principal, task.ConversationID, task.RequestID); err != nil {
		return s.releaseAnswerJob(ctx, &job, err)
	} else if ok {
		return s.completeAnswerJob(ctx, &job, recorded.Status, recorded.ErrorCode)
	}
	conversation, err := s.loadConversation(ctx, principal, task.ConversationID)
	if err != nil {
		return s.failAnswerJob(ctx, &job, CodeForbidden, err)
	}
	stored, err := decodeAuthorizationScope(job.ScopeJSON)
	if err != nil {
		return s.failAnswerJob(ctx, &job, CodeForbidden, err)
	}
	// Rebuild the scope from current membership and intersect it with the scope
	// recorded at acceptance. A revoked membership or API-key grant therefore
	// narrows the worker's access instead of being replayed from the queue.
	fresh, err := s.authorizedKnowledgeBase(ctx, principal, conversation.KnowledgeBaseID)
	if err != nil {
		return s.failAnswerJob(ctx, &job, CodeForbidden, err)
	}
	scope := intersectAuthorizationScope(fresh, stored)
	if !containsString(scope.KnowledgeBaseIDs, conversation.KnowledgeBaseID) {
		return s.failAnswerJob(ctx, &job, CodeForbidden, apperror.ErrForbidden)
	}
	chatModel := ""
	if s.models != nil {
		resolved, err := s.models.ChatModel(ctx, principal.TenantID, conversation.KnowledgeBaseID)
		if err != nil {
			return s.releaseAnswerJob(ctx, &job, err)
		}
		chatModel = resolved
	}
	answerModel := chatModel
	if strings.TrimSpace(answerModel) == "" {
		answerModel = s.chatName()
	}
	history, err := s.loadHistory(ctx, principal, conversation.ID)
	if err != nil {
		history = nil
	}
	query := s.conversationQuery(ctx, job, task)
	if strings.TrimSpace(query) == "" {
		return s.failAnswerJob(ctx, &job, CodeJobInvalid, fmt.Errorf("answer job has no recorded question"))
	}
	prepared := preparedAnswer{
		conversation: conversation, principal: principal, scope: scope,
		requestID: task.RequestID, query: query, sessionOverride: task.SessionOverride,
		history: history, chatModel: chatModel, answerModel: answerModel, started: time.Now(),
	}
	result, execErr := s.execute(ctx, prepared, s.jobEvents(ctx, &job, nil))
	if execErr != nil {
		return s.releaseAnswerJob(ctx, &job, execErr)
	}
	return s.completeAnswerJob(ctx, &job, result.Status, result.ErrorCode)
}

// conversationQuery reads the question back from the user message persisted by
// PrepareAnswer. The queue payload intentionally does not carry prompt text.
func (s *Service) conversationQuery(ctx context.Context, job models.AnswerJob, task tasks.Answer) string {
	content, ok, err := s.messages.LatestQuestion(ctx, job.TenantID, job.UserID, task.ConversationID, task.RequestID)
	if err != nil || !ok {
		return ""
	}
	return content
}

// SweepStaleAnswers fails jobs whose worker lease expired and appends a terminal
// error to their event stream, so an SSE tail (or a reconnect) always ends.
func (s *Service) SweepStaleAnswers(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.jobs.SweepStale(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		job := models.AnswerJob{ID: row.ID, TenantID: row.TenantID, UserID: row.UserID, RequestID: row.RequestID}
		emit := s.jobEvents(ctx, &job, nil)
		_ = emit(EventError, map[string]any{"code": CodeWorkerLost, "message": "the worker lease expired before the answer completed"})
		_ = emit(EventComplete, map[string]any{"status": AnswerFailed, "code": CodeWorkerLost})
	}
	return int64(len(rows)), nil
}

// FailAnswerJob is the terminal hook for a queue task whose retry budget is
// spent. A job that already reached a terminal state is left alone.
func (s *Service) FailAnswerJob(ctx context.Context, jobID, code, message string) error {
	if strings.TrimSpace(jobID) == "" {
		return nil
	}
	job, err := s.jobs.Load(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status == AnswerJobCompleted || job.Status == AnswerJobFailed {
		return nil
	}
	return s.failAnswerJob(ctx, &job, code, errors.New(message))
}

func (s *Service) ensureAnswerJob(ctx context.Context, prepared preparedAnswer) (models.AnswerJob, bool, error) {
	jobID, err := newID("job")
	if err != nil {
		return models.AnswerJob{}, false, err
	}
	scope, err := json.Marshal(prepared.scope)
	if err != nil {
		return models.AnswerJob{}, false, fmt.Errorf("marshal answer scope: %w", err)
	}
	value := string(scope)
	job := models.AnswerJob{
		ID: jobID, TenantID: prepared.principal.TenantID, UserID: prepared.principal.SubjectID,
		ConversationID: prepared.conversation.ID, RequestID: prepared.requestID,
		Status: AnswerJobQueued, ScopeJSON: &value,
	}
	return s.jobs.CreateOrLoad(ctx, job)
}

// claimAnswerJob admits one worker per job and renews the lease atomically.
func (s *Service) claimAnswerJob(ctx context.Context, task tasks.Answer) (models.AnswerJob, bool, error) {
	now := time.Now()
	job, claimed, err := s.jobs.Claim(ctx, task.JobID, task.TenantID, now.Add(s.leaseTTL), now)
	if err != nil {
		return models.AnswerJob{}, false, err
	}
	if claimed {
		return job, true, nil
	}
	exists, err := s.jobs.Exists(ctx, task.JobID, task.TenantID)
	if err != nil {
		return models.AnswerJob{}, false, err
	}
	if !exists {
		return models.AnswerJob{}, false, errAnswerJobNotVisible
	}
	return models.AnswerJob{}, false, nil
}

func (s *Service) requeueAnswerJob(ctx context.Context, job *models.AnswerJob) error {
	if err := s.jobs.Update(ctx, job.ID, job.TenantID, map[string]any{
		"status": AnswerJobQueued, "lease_owner": "", "lease_until": nil, "error_code": "", "updated_at": time.Now(),
	}); err != nil {
		return fmt.Errorf("requeue answer job: %w", err)
	}
	job.Status = AnswerJobQueued
	job.LeaseOwner = ""
	job.LeaseUntil = nil
	job.ErrorCode = ""
	return nil
}

func (s *Service) releaseAnswerJob(ctx context.Context, job *models.AnswerJob, cause error) error {
	if err := s.jobs.Update(ctx, job.ID, job.TenantID, map[string]any{
		"status": AnswerJobQueued, "lease_owner": "", "lease_until": nil,
		"error_code": CodeWorkerError, "updated_at": time.Now(),
	}); err != nil {
		return fmt.Errorf("release answer job %s: %w (original: %v)", job.ID, err, cause)
	}
	return cause
}

func (s *Service) completeAnswerJob(ctx context.Context, job *models.AnswerJob, answerStatus, code string) error {
	status := AnswerJobCompleted
	switch answerStatus {
	case AnswerFailed, AnswerTimedOut, AnswerCancelled:
		status = AnswerJobFailed
	}
	if err := s.jobs.Update(ctx, job.ID, job.TenantID, map[string]any{
		"status": status, "error_code": code, "lease_owner": "", "lease_until": nil, "updated_at": time.Now(),
	}); err != nil {
		return fmt.Errorf("complete answer job %s: %w", job.ID, err)
	}
	return nil
}

// abortAnswerJob marks a job failed after the request could not enqueue or
// record its pre-flight work. It returns the original error.
func (s *Service) abortAnswerJob(ctx context.Context, job *models.AnswerJob, code string, cause error) error {
	_ = s.failAnswerJob(ctx, job, code, cause)
	return cause
}

// failAnswerJob records a terminal failure and appends the terminal events the
// SSE tail waits for. Event failures are best effort: the job row is the
// durable signal.
func (s *Service) failAnswerJob(ctx context.Context, job *models.AnswerJob, code string, cause error) error {
	if err := s.jobs.Update(ctx, job.ID, job.TenantID, map[string]any{
		"status": AnswerJobFailed, "error_code": code, "lease_owner": "", "lease_until": nil, "updated_at": time.Now(),
	}); err != nil {
		return fmt.Errorf("fail answer job %s: %w", job.ID, err)
	}
	emit := s.jobEvents(ctx, job, nil)
	_ = emit(EventError, map[string]any{"code": code, "message": cause.Error()})
	_ = emit(EventComplete, map[string]any{"status": AnswerFailed, "code": code})
	return nil
}

// jobEvents appends events at the job's shared sequence. emit is nil for worker
// runs; the HTTP tail replays the persisted rows.
func (s *Service) jobEvents(ctx context.Context, job *models.AnswerJob, emit Emitter) func(string, map[string]any) error {
	return func(eventType string, data map[string]any) error {
		sequence, err := s.nextAnswerSequence(ctx, job.ID, job.TenantID)
		if err != nil {
			return err
		}
		return s.appendEventAt(ctx, job.RequestID, job.TenantID, job.UserID, sequence, eventType, data, emit)
	}
}

func (s *Service) nextAnswerSequence(ctx context.Context, jobID, tenantID string) (int, error) {
	return s.jobs.NextSequence(ctx, jobID, tenantID)
}

func decodeAuthorizationScope(value *string) (authz.AuthorizationScope, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return authz.AuthorizationScope{}, fmt.Errorf("answer job has no recorded authorization scope")
	}
	var scope authz.AuthorizationScope
	if err := json.Unmarshal([]byte(*value), &scope); err != nil {
		return authz.AuthorizationScope{}, fmt.Errorf("decode answer authorization scope: %w", err)
	}
	return scope, nil
}

// intersectAuthorizationScope keeps the narrower of the current role scope and
// the scope accepted with the request. Only the current ACL version is valid.
func intersectAuthorizationScope(fresh, stored authz.AuthorizationScope) authz.AuthorizationScope {
	knowledgeBases := intersectStrings(fresh.KnowledgeBaseIDs, stored.KnowledgeBaseIDs)
	capabilities := intersectStrings(fresh.Capabilities, stored.Capabilities)
	return authz.AuthorizationScope{
		SubjectID: fresh.SubjectID, TenantID: fresh.TenantID,
		KnowledgeBaseIDs: knowledgeBases, ResourceIDs: knowledgeBases,
		Capabilities: capabilities, ACLVersion: fresh.ACLVersion,
	}
}

func intersectStrings(left, right []string) []string {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(right))
	for _, value := range right {
		allowed[value] = true
	}
	result := make([]string, 0, len(left))
	for _, value := range left {
		if allowed[value] {
			result = append(result, value)
		}
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
