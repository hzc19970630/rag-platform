package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/contracts/tasks"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

func TestAnswerJobStoreLifecycle(t *testing.T) {
	db := newTestConversationDB(t)
	store := NewPostgresAnswerJobStore(db)
	ctx := context.Background()
	now := time.Now().UTC()
	job := models.AnswerJob{
		ID: "job-1", TenantID: "tenant-a", UserID: "user-a", ConversationID: "conv-1",
		RequestID: "req-1", Status: AnswerJobQueued, CreatedAt: now, UpdatedAt: now,
	}

	created, isNew, err := store.CreateOrLoad(ctx, job)
	if err != nil || !isNew || created.ID != "job-1" {
		t.Fatalf("CreateOrLoad = %+v, %v, %v", created, isNew, err)
	}
	duplicate := job
	duplicate.ID = "job-2"
	existing, isNew, err := store.CreateOrLoad(ctx, duplicate)
	if err != nil || isNew || existing.ID != "job-1" {
		t.Fatalf("duplicate CreateOrLoad = %+v, %v, %v", existing, isNew, err)
	}

	claimed, ok, err := store.Claim(ctx, "job-1", "tenant-a", now.Add(time.Minute), now)
	if err != nil || !ok || claimed.Attempts != 1 || claimed.Status != AnswerJobRunning {
		t.Fatalf("Claim = %+v, %v, %v", claimed, ok, err)
	}
	if _, ok, err := store.Claim(ctx, "job-1", "tenant-a", now.Add(time.Minute), now); err != nil || ok {
		t.Fatalf("second Claim = %v, %v; want held lease", ok, err)
	}
	if ok, err := store.Exists(ctx, "job-1", "tenant-a"); err != nil || !ok {
		t.Fatalf("Exists = %v, %v", ok, err)
	}
	if ok, err := store.Exists(ctx, "job-1", "tenant-b"); err != nil || ok {
		t.Fatalf("cross-tenant Exists = %v, %v", ok, err)
	}
	if _, err := store.Load(ctx, "missing"); err == nil {
		t.Fatal("Load missing must fail")
	}
	if err := store.Update(ctx, "job-1", "tenant-a", map[string]any{"status": AnswerJobRunning, "error_code": "probe"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	loaded, err := store.Load(ctx, "job-1")
	if err != nil || loaded.ErrorCode != "probe" {
		t.Fatalf("Load = %+v, %v", loaded, err)
	}
	first, err := store.NextSequence(ctx, "job-1", "tenant-a")
	if err != nil || first != 1 {
		t.Fatalf("NextSequence = %d, %v", first, err)
	}
	second, err := store.NextSequence(ctx, "job-1", "tenant-a")
	if err != nil || second != 2 {
		t.Fatalf("NextSequence = %d, %v", second, err)
	}

	past := now.Add(-time.Minute)
	if err := store.Update(ctx, "job-1", "tenant-a", map[string]any{"lease_until": &past}); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	stale, err := store.SweepStale(ctx, now, 10)
	if err != nil || len(stale) != 1 || stale[0].ID != "job-1" {
		t.Fatalf("SweepStale = %+v, %v", stale, err)
	}
	loaded, err = store.Load(ctx, "job-1")
	if err != nil || loaded.Status != AnswerJobFailed || loaded.ErrorCode != CodeWorkerLost {
		t.Fatalf("swept job = %+v, %v", loaded, err)
	}

	if _, ok, err := store.Claim(ctx, "job-1", "tenant-a", now.Add(time.Minute), now); err != nil || ok {
		t.Fatalf("failed job must not be claimable: %v, %v", ok, err)
	}
}

func TestSessionStoreLifecycle(t *testing.T) {
	db := newTestConversationDB(t)
	store := NewPostgresSessionStore(db)
	ctx := context.Background()
	now := time.Now().UTC()
	conversations := []models.Conversation{
		{ID: "conv-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", UserID: "user-a", Title: "One", CreatedAt: now, UpdatedAt: now},
		{ID: "conv-2", TenantID: "tenant-a", KnowledgeBaseID: "kb-b", UserID: "user-a", Title: "Two", CreatedAt: now, UpdatedAt: now.Add(time.Minute)},
		{ID: "conv-3", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", UserID: "user-b", Title: "Other", CreatedAt: now, UpdatedAt: now},
	}
	for index := range conversations {
		if err := store.Create(ctx, conversations[index]); err != nil {
			t.Fatalf("Create conversation: %v", err)
		}
	}
	rows, err := store.List(ctx, "tenant-a", "user-a", []string{"kb-a", "kb-b"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("List = %d, %v", len(rows), err)
	}
	rows, err = store.List(ctx, "tenant-a", "user-a", []string{"kb-a"})
	if err != nil || len(rows) != 1 || rows[0].ID != "conv-1" {
		t.Fatalf("filtered List = %+v, %v", rows, err)
	}
	if _, err := store.Load(ctx, "tenant-a", "user-b", "conv-1"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("cross-user Load = %v, want not found", err)
	}
	loaded, err := store.Load(ctx, "tenant-a", "user-a", "conv-1")
	if err != nil || loaded.Title != "One" {
		t.Fatalf("Load = %+v, %v", loaded, err)
	}

	messages := []models.Message{
		{ID: "msg-2", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", ConversationID: "conv-1", UserID: "user-a", Role: MessageRoleAssistant, Content: "second", Status: MessageStatusCompleted, CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)},
		{ID: "msg-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", ConversationID: "conv-1", UserID: "user-a", Role: MessageRoleUser, Content: "first", Status: MessageStatusCompleted, CreatedAt: now, UpdatedAt: now},
	}
	for index := range messages {
		if err := db.Create(&messages[index]).Error; err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}
	listed, err := store.Messages(ctx, "tenant-a", "conv-1")
	if err != nil || len(listed) != 2 || listed[0].ID != "msg-1" || listed[1].ID != "msg-2" {
		t.Fatalf("Messages = %+v, %v", listed, err)
	}
	if err := store.Touch(ctx, "tenant-a", "conv-1", map[string]any{"title": "Renamed"}); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	loaded, err = store.Load(ctx, "tenant-a", "user-a", "conv-1")
	if err != nil || loaded.Title != "Renamed" {
		t.Fatalf("touched conversation = %+v, %v", loaded, err)
	}
}

func TestConversationServiceSessionReads(t *testing.T) {
	service, db, _, _ := newAsyncFixture(t, "unused")
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "dev"}

	if _, err := service.CreateConversation(ctx, principal, CreateConversationInput{}); err == nil {
		t.Fatal("empty knowledge base must be rejected")
	}
	if _, err := service.CreateConversation(ctx, principal, CreateConversationInput{KnowledgeBaseID: "kb-b"}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("unauthorized knowledge base = %v, want forbidden", err)
	}
	conversation, err := service.CreateConversation(ctx, principal, CreateConversationInput{KnowledgeBaseID: "kb-a", Title: strings.Repeat("t", 500)})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if len([]rune(conversation.Title)) > conversationTitleMaxRunes {
		t.Fatalf("title was not truncated: %d", len([]rune(conversation.Title)))
	}
	listed, err := service.ListConversations(ctx, principal)
	if err != nil || len(listed) != 1 || listed[0].ID != conversation.ID {
		t.Fatalf("ListConversations = %+v, %v", listed, err)
	}
	if _, err := service.ListConversations(ctx, authz.Principal{SubjectID: "ghost", TenantID: "tenant-a"}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("ghost ListConversations = %v, want forbidden", err)
	}

	now := time.Now().UTC()
	if err := db.Create(&models.Message{
		ID: "msg-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", ConversationID: conversation.ID,
		UserID: "user-a", RequestID: "req-1", Role: MessageRoleUser, Content: "hello",
		Status: MessageStatusCompleted, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := db.Create(&models.MessageCitation{
		ID: "cit-1", MessageID: "msg-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a",
		DocumentID: "doc-1", DocumentVersionID: "ver-1", ChunkID: "chunk-1",
		CitationIndex: 1, Rank: 1, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed citation: %v", err)
	}
	views, err := service.ListMessages(ctx, principal, conversation.ID)
	if err != nil || len(views) != 1 || len(views[0].Citations) != 1 {
		t.Fatalf("ListMessages = %+v, %v", views, err)
	}
	if _, err := service.ListMessages(ctx, principal, ""); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("empty conversation id = %v, want not found", err)
	}
	if _, err := service.ListMessages(ctx, authz.Principal{SubjectID: "user-b", TenantID: "tenant-a"}, conversation.ID); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("cross-user messages = %v, want not found", err)
	}
}

func TestFailAnswerJobIsTerminalAndIdempotent(t *testing.T) {
	service, db, _, _ := newAsyncFixture(t, "unused")
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "dev"}
	conversation, err := service.CreateConversation(ctx, principal, CreateConversationInput{KnowledgeBaseID: "kb-a", Title: "failure"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	prepared, err := service.PrepareAnswer(ctx, principal, AskInput{
		ConversationID: conversation.ID, Query: "question", RequestID: "req-fail",
	})
	if err != nil {
		t.Fatalf("PrepareAnswer: %v", err)
	}
	var job models.AnswerJob
	if err := db.First(&job, "request_id = ?", prepared.RequestID).Error; err != nil {
		t.Fatalf("load job: %v", err)
	}
	if service.AsyncEnabled() != true {
		t.Fatal("async should be enabled in the fixture")
	}
	if err := service.FailAnswerJob(ctx, "", "code", "message"); err != nil {
		t.Fatalf("empty FailAnswerJob = %v", err)
	}
	if err := service.FailAnswerJob(ctx, "missing", "code", "message"); err == nil {
		t.Fatal("missing job must return the store error")
	}
	if err := service.FailAnswerJob(ctx, job.ID, CodeWorkerError, "boom"); err != nil {
		t.Fatalf("FailAnswerJob: %v", err)
	}
	if err := db.First(&job, "id = ?", job.ID).Error; err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.Status != AnswerJobFailed || job.ErrorCode != CodeWorkerError {
		t.Fatalf("failed job = %+v", job)
	}
	var events int64
	if err := db.Model(&models.StreamEvent{}).Where("request_id = ?", job.RequestID).Count(&events).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events < 2 {
		t.Fatalf("terminal events = %d, want at least 2", events)
	}
	if err := service.FailAnswerJob(ctx, job.ID, CodeWorkerError, "again"); err != nil {
		t.Fatalf("second FailAnswerJob = %v", err)
	}
}

func TestRunAnswerIsIdempotentAfterCompletion(t *testing.T) {
	service, db, chat, publisher := newAsyncFixture(t, "The answer is 42 [1].")
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "dev"}
	conversation, err := service.CreateConversation(ctx, principal, CreateConversationInput{KnowledgeBaseID: "kb-a", Title: "idempotent"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if _, err := service.PrepareAnswer(ctx, principal, AskInput{
		ConversationID: conversation.ID, Query: "What is the answer?", RequestID: "req-run",
	}); err != nil {
		t.Fatalf("PrepareAnswer: %v", err)
	}
	task := publisher.tasks[0]
	if err := service.RunAnswer(ctx, task); err != nil {
		t.Fatalf("RunAnswer: %v", err)
	}
	calls := chat.calls
	if err := service.RunAnswer(ctx, task); err != nil {
		t.Fatalf("repeat RunAnswer: %v", err)
	}
	if chat.calls != calls {
		t.Fatalf("repeat RunAnswer generated another answer: %d -> %d", calls, chat.calls)
	}
	var job models.AnswerJob
	if err := db.First(&job, "id = ?", task.JobID).Error; err != nil {
		t.Fatalf("load job: %v", err)
	}
	if job.Status != AnswerJobCompleted {
		t.Fatalf("job status = %s, want completed", job.Status)
	}
	if err := service.RunAnswer(ctx, tasks.Answer{}); err == nil {
		t.Fatal("task without identity must be rejected")
	}
}

func TestSweepStaleAnswersFailsExpiredJobs(t *testing.T) {
	service, db, _, _ := newAsyncFixture(t, "unused")
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-time.Minute)
	job := models.AnswerJob{
		ID: "job-stale", TenantID: "tenant-a", UserID: "user-a", ConversationID: "conv-a",
		RequestID: "req-stale", Status: AnswerJobRunning, LeaseOwner: "worker", LeaseUntil: &past,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatalf("seed stale job: %v", err)
	}
	purged, err := service.SweepStaleAnswers(ctx, now, 10)
	if err != nil || purged != 1 {
		t.Fatalf("SweepStaleAnswers = %d, %v", purged, err)
	}
	if err := db.First(&job, "id = ?", "job-stale").Error; err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if job.Status != AnswerJobFailed || job.ErrorCode != CodeWorkerLost {
		t.Fatalf("swept job = %+v", job)
	}
}

func newTestConversationDB(t *testing.T) *gorm.DB {
	t.Helper()
	_, db, _, _ := newAsyncFixture(t, "unused")
	return db
}

type failingAnswerPublisher struct{ err error }

func (p failingAnswerPublisher) PublishAnswer(context.Context, tasks.Answer) error { return p.err }

func TestPrepareAnswerRequiresPublisherAndReportsQueueFailure(t *testing.T) {
	service, db, _, _ := newAsyncFixture(t, "unused")
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "dev"}
	conversation, err := service.CreateConversation(ctx, principal, CreateConversationInput{KnowledgeBaseID: "kb-a", Title: "publisher"})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	noPublisher := NewService(Options{
		Stores: NewPostgresStores(db), Authz: service.authz, Retrieval: service.retrieval,
		Chat: service.chat, Prompts: service.prompts, Timeout: 5 * time.Second, AsyncEnabled: true,
	})
	if _, err := noPublisher.PrepareAnswer(ctx, principal, AskInput{ConversationID: conversation.ID, Query: "q", RequestID: "req-no-publisher"}); err == nil || !strings.Contains(err.Error(), "publisher is not configured") {
		t.Fatalf("PrepareAnswer without publisher = %v", err)
	}

	service.answers = failingAnswerPublisher{err: errors.New("queue down")}
	if _, err := service.PrepareAnswer(ctx, principal, AskInput{ConversationID: conversation.ID, Query: "q", RequestID: "req-queue-down"}); err == nil || !strings.Contains(err.Error(), "enqueue answer job") {
		t.Fatalf("PrepareAnswer with failing publisher = %v", err)
	}
	var failed models.AnswerJob
	if err := db.First(&failed, "request_id = ?", "req-queue-down").Error; err != nil {
		t.Fatalf("load failed job: %v", err)
	}
	if failed.Status != AnswerJobFailed || failed.ErrorCode != CodeQueueUnavailable {
		t.Fatalf("failed job = %+v", failed)
	}

	if purged, err := service.SweepStaleAnswers(ctx, time.Now().UTC(), 0); err != nil || purged != 0 {
		t.Fatalf("empty sweep = %d, %v", purged, err)
	}
}
