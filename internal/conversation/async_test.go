package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/example/rag-platform/contracts/tasks"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

type fakeAnswerPublisher struct {
	tasks []tasks.Answer
}

func (p *fakeAnswerPublisher) PublishAnswer(_ context.Context, task tasks.Answer) error {
	p.tasks = append(p.tasks, task)
	return nil
}

// newAsyncFixture reuses the end-to-end Ask fixture (real authz, retrieval and
// prompt registry) and swaps in an async service backed by a fake publisher.
func newAsyncFixture(t *testing.T, answer string) (*Service, *gorm.DB, *fixedChatter, *fakeAnswerPublisher) {
	t.Helper()
	synchronous, db, chat := newAskFixture(t, answer)
	if err := db.AutoMigrate(&models.AnswerJob{}); err != nil {
		t.Fatalf("migrate answer jobs: %v", err)
	}
	publisher := &fakeAnswerPublisher{}
	service := NewService(Options{
		DB: db, Authz: synchronous.authz, Retrieval: synchronous.retrieval,
		Chat: chat, Prompts: synchronous.prompts, Timeout: 5 * time.Second,
		AnswerPublisher: publisher, AsyncEnabled: true, AnswerLease: time.Minute,
	})
	return service, db, chat, publisher
}

func TestRunAnswerRetriesWhenJobIsNotYetVisible(t *testing.T) {
	service, _, _, _ := newAsyncFixture(t, "unused")
	err := service.RunAnswer(context.Background(), tasks.Answer{JobID: "missing-job", TenantID: "tenant-a"})
	if !errors.Is(err, errAnswerJobNotVisible) {
		t.Fatalf("a task consumed before the request transaction commits must be retryable, got %v", err)
	}
}

func TestPrepareAnswerEnqueuesOnceAndRunAnswerCompletes(t *testing.T) {
	service, db, chat, publisher := newAsyncFixture(t, "The answer is 42 [1].")
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "dev"}
	// A title avoids a model-backed title call, so chat.calls counts generation
	// exactly once.
	conversation, err := service.CreateConversation(ctx, principal, CreateConversationInput{KnowledgeBaseID: "kb-a", Title: "async"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	prepared, err := service.PrepareAnswer(ctx, principal, AskInput{
		ConversationID: conversation.ID, Query: "What is the answer?", RequestID: "req-async",
	})
	if err != nil {
		t.Fatalf("prepare answer: %v", err)
	}
	if prepared.RequestID != "req-async" || prepared.Existing != nil {
		t.Fatalf("unexpected prepare result: %+v", prepared)
	}
	if len(publisher.tasks) != 1 {
		t.Fatalf("expected one queued task, got %d", len(publisher.tasks))
	}
	task := publisher.tasks[0]
	if task.TenantID != "tenant-a" || task.SubjectID != "user-a" || task.RequestID != "req-async" || task.JobID == "" {
		t.Fatalf("unexpected task payload: %+v", task)
	}
	var job models.AnswerJob
	if err := db.Where("id = ?", task.JobID).First(&job).Error; err != nil {
		t.Fatalf("load answer job: %v", err)
	}
	if job.Status != AnswerJobQueued || job.NextSequence != 3 {
		t.Fatalf("pre-flight must persist three events and stay queued: %+v", job)
	}

	if err := service.RunAnswer(ctx, task); err != nil {
		t.Fatalf("run answer: %v", err)
	}
	if chat.calls != 1 {
		t.Fatalf("expected one generation call, got %d", chat.calls)
	}
	if err := db.Where("id = ?", task.JobID).First(&job).Error; err != nil {
		t.Fatalf("reload answer job: %v", err)
	}
	if job.Status != AnswerJobCompleted || job.ErrorCode != "" {
		t.Fatalf("job must be terminal after the answer: %+v", job)
	}
	var assistant models.Message
	if err := db.Where("conversation_id = ? AND role = ?", conversation.ID, MessageRoleAssistant).First(&assistant).Error; err != nil {
		t.Fatalf("load assistant message: %v", err)
	}
	if assistant.Status != MessageStatusCompleted || assistant.Content != "The answer is 42 [1]." {
		t.Fatalf("unexpected assistant message: %+v", assistant)
	}

	events, err := service.ReplayEvents(ctx, principal, "req-async", 0)
	if err != nil {
		t.Fatalf("replay events: %v", err)
	}
	if len(events) < 5 {
		t.Fatalf("expected pre-flight plus execution events, got %d", len(events))
	}
	for index := 1; index < len(events); index++ {
		if events[index].Sequence != events[index-1].Sequence+1 {
			t.Fatalf("event sequence is not contiguous at %d: %+v", index, events)
		}
	}
	if events[0].Data["state"] != AnswerReceived || events[1].Data["state"] != AnswerAuthorized || events[2].Data["state"] != AnswerPlanned {
		t.Fatalf("pre-flight event order changed: %+v", events[:3])
	}
	if events[3].Data["state"] != AnswerRetrieving {
		t.Fatalf("worker must continue the stored sequence with RETRIEVING: %+v", events[3])
	}
	if events[len(events)-1].Type != EventComplete {
		t.Fatalf("terminal event = %s, want %s", events[len(events)-1].Type, EventComplete)
	}

	// A duplicate delivery must not generate a second answer.
	if err := service.RunAnswer(ctx, task); err != nil {
		t.Fatalf("duplicate run: %v", err)
	}
	if chat.calls != 1 {
		t.Fatalf("duplicate delivery generated twice: %d", chat.calls)
	}

	// A retried request id returns the recorded answer without a new task.
	replayed, err := service.PrepareAnswer(ctx, principal, AskInput{
		ConversationID: conversation.ID, Query: "What is the answer?", RequestID: "req-async",
	})
	if err != nil {
		t.Fatalf("retry prepare: %v", err)
	}
	if replayed.Existing == nil || replayed.Existing.MessageID != assistant.ID {
		t.Fatalf("retry must return the recorded answer: %+v", replayed)
	}
	if len(publisher.tasks) != 1 {
		t.Fatalf("retry enqueued another task: %d", len(publisher.tasks))
	}
}

func TestSweepStaleAnswersProducesTerminalErrorAndAllowsRetry(t *testing.T) {
	service, db, _, publisher := newAsyncFixture(t, "unused")
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "dev"}
	conversation, err := service.CreateConversation(ctx, principal, CreateConversationInput{KnowledgeBaseID: "kb-a"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := service.PrepareAnswer(ctx, principal, AskInput{
		ConversationID: conversation.ID, Query: "What is the answer?", RequestID: "req-lost",
	}); err != nil {
		t.Fatalf("prepare answer: %v", err)
	}
	task := publisher.tasks[0]
	claimed, ok, err := service.claimAnswerJob(ctx, task)
	if err != nil || !ok {
		t.Fatalf("claim job: ok=%v err=%v", ok, err)
	}
	if claimed.Status != AnswerJobRunning {
		t.Fatalf("claim must mark the job running: %+v", claimed)
	}
	// Simulate a worker that died before finishing: the lease is already stale.
	past := time.Now().Add(-time.Minute)
	if err := db.Model(&models.AnswerJob{}).Where("id = ?", task.JobID).Update("lease_until", past).Error; err != nil {
		t.Fatalf("age the lease: %v", err)
	}
	swept, err := service.SweepStaleAnswers(ctx, time.Now(), 10)
	if err != nil || swept != 1 {
		t.Fatalf("sweep stale answers: swept=%d err=%v", swept, err)
	}
	var job models.AnswerJob
	if err := db.Where("id = ?", task.JobID).First(&job).Error; err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.Status != AnswerJobFailed || job.ErrorCode != CodeWorkerLost {
		t.Fatalf("stale job must fail with worker_lost: %+v", job)
	}
	events, err := service.ReplayEvents(ctx, principal, "req-lost", 0)
	if err != nil {
		t.Fatalf("replay events: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("stale sweep must append terminal events, got %d", len(events))
	}
	last := events[len(events)-1]
	previous := events[len(events)-2]
	if previous.Type != EventError || previous.Data["code"] != CodeWorkerLost {
		t.Fatalf("expected worker_lost error before completion: %+v", previous)
	}
	if last.Type != EventComplete || last.Data["status"] != AnswerFailed {
		t.Fatalf("expected failed completion terminal: %+v", last)
	}

	// Reusing the request id after a terminal failure starts a fresh attempt.
	if _, err := service.PrepareAnswer(ctx, principal, AskInput{
		ConversationID: conversation.ID, Query: "What is the answer?", RequestID: "req-lost",
	}); err != nil {
		t.Fatalf("retry failed job: %v", err)
	}
	if len(publisher.tasks) != 2 {
		t.Fatalf("failed job was not requeued: %d tasks", len(publisher.tasks))
	}
	if err := db.Where("id = ?", task.JobID).First(&job).Error; err != nil {
		t.Fatalf("reload requeued job: %v", err)
	}
	if job.Status != AnswerJobQueued {
		t.Fatalf("requeued job status = %s, want %s", job.Status, AnswerJobQueued)
	}
}

func TestIntersectAuthorizationScopeNarrowsToCurrentACL(t *testing.T) {
	fresh := authz.AuthorizationScope{
		SubjectID: "user-a", TenantID: "tenant-a",
		KnowledgeBaseIDs: []string{"kb-a", "kb-b"}, Capabilities: []string{"read", "write"},
		ACLVersion: "tenant:v2",
	}
	stored := authz.AuthorizationScope{
		SubjectID: "user-a", TenantID: "tenant-a",
		KnowledgeBaseIDs: []string{"kb-a", "kb-c"}, Capabilities: []string{"read"},
		ACLVersion: "tenant:v1",
	}
	scope := intersectAuthorizationScope(fresh, stored)
	if len(scope.KnowledgeBaseIDs) != 1 || scope.KnowledgeBaseIDs[0] != "kb-a" {
		t.Fatalf("knowledge-base intersection wrong: %+v", scope)
	}
	if len(scope.Capabilities) != 1 || scope.Capabilities[0] != "read" {
		t.Fatalf("capability intersection wrong: %+v", scope)
	}
	if scope.ACLVersion != "tenant:v2" {
		t.Fatalf("only the fresh ACL version is valid, got %s", scope.ACLVersion)
	}
}
