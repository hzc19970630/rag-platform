package conversation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/contracts/model"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

type blockingChatter struct {
	started chan struct{}
	once    sync.Once
}

func (c *blockingChatter) Complete(ctx context.Context, _ model.ChatRequest) (model.ChatResponse, error) {
	c.once.Do(func() { close(c.started) })
	<-ctx.Done()
	return model.ChatResponse{}, ctx.Err()
}

func (c *blockingChatter) Name() string { return "blocking-chat" }

func newFailureFixture(t *testing.T, chatter Chatter, timeout time.Duration) (*Service, *gorm.DB, *fakeAnswerPublisher) {
	t.Helper()
	synchronous, db, _ := newAskFixture(t, "unused")
	if err := db.AutoMigrate(&models.AnswerJob{}); err != nil {
		t.Fatalf("migrate answer jobs: %v", err)
	}
	publisher := &fakeAnswerPublisher{}
	service := NewService(Options{
		Stores: NewPostgresStores(db), Authz: synchronous.authz, Retrieval: synchronous.retrieval,
		Chat: chatter, Prompts: synchronous.prompts, Timeout: timeout,
		AnswerPublisher: publisher, AsyncEnabled: true, AnswerLease: time.Minute,
	})
	return service, db, publisher
}

func prepareFailureTask(t *testing.T, service *Service, publisher *fakeAnswerPublisher, requestID string) (authz.Principal, string, string) {
	t.Helper()
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "dev"}
	conversation, err := service.CreateConversation(context.Background(), principal, CreateConversationInput{KnowledgeBaseID: "kb-a", Title: "failure"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := service.PrepareAnswer(context.Background(), principal, AskInput{
		ConversationID: conversation.ID, Query: "What is the answer?", RequestID: requestID,
	}); err != nil {
		t.Fatalf("prepare answer: %v", err)
	}
	if len(publisher.tasks) != 1 {
		t.Fatalf("expected one queued task, got %d", len(publisher.tasks))
	}
	return principal, conversation.ID, publisher.tasks[0].JobID
}

func assertTerminalFailure(t *testing.T, service *Service, db *gorm.DB, principal authz.Principal, jobID, requestID, wantMessageStatus, wantCode string) {
	t.Helper()
	var job models.AnswerJob
	if err := db.First(&job, "id = ?", jobID).Error; err != nil {
		t.Fatalf("load answer job: %v", err)
	}
	if job.Status != AnswerJobFailed {
		t.Fatalf("job status = %s, want %s", job.Status, AnswerJobFailed)
	}
	var assistant models.Message
	if err := db.Where("request_id = ? AND role = ?", requestID, MessageRoleAssistant).First(&assistant).Error; err != nil {
		t.Fatalf("terminal answer must be persisted: %v", err)
	}
	if assistant.Status != wantMessageStatus || assistant.ErrorCode != wantCode {
		t.Fatalf("terminal message = status %s code %s, want %s/%s", assistant.Status, assistant.ErrorCode, wantMessageStatus, wantCode)
	}
	events, err := service.ReplayEvents(context.Background(), principal, requestID, 0)
	if err != nil {
		t.Fatalf("replay events: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Type != EventComplete {
		t.Fatalf("failure must end with a terminal event: %+v", events)
	}
}

func TestRunAnswerCancellationPersistsTerminalState(t *testing.T) {
	chatter := &blockingChatter{started: make(chan struct{})}
	service, db, publisher := newFailureFixture(t, chatter, 5*time.Second)
	principal, _, jobID := prepareFailureTask(t, service, publisher, "req-cancel")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.RunAnswer(ctx, publisher.tasks[0]) }()
	select {
	case <-chatter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("generation did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunAnswer must record the terminal state instead of returning an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunAnswer did not observe cancellation")
	}
	assertTerminalFailure(t, service, db, principal, jobID, "req-cancel", AnswerCancelled, CodeClientDisconnected)
}

func TestRunAnswerTimeoutPersistsTerminalState(t *testing.T) {
	chatter := &blockingChatter{started: make(chan struct{})}
	service, db, publisher := newFailureFixture(t, chatter, 150*time.Millisecond)
	principal, _, jobID := prepareFailureTask(t, service, publisher, "req-timeout")

	if err := service.RunAnswer(context.Background(), publisher.tasks[0]); err != nil {
		t.Fatalf("run answer: %v", err)
	}
	assertTerminalFailure(t, service, db, principal, jobID, "req-timeout", AnswerTimedOut, CodeTimeout)
}
