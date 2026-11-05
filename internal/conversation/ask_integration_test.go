package conversation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/contracts/model"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/prompts"
	"github.com/example/rag-platform/internal/retrieval"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fixedKeywordSearcher struct {
	hits []retrieval.Hit
}

func (s fixedKeywordSearcher) SearchKeyword(_ context.Context, _ retrieval.RecallRequest) ([]retrieval.Hit, error) {
	return s.hits, nil
}

type fixedChatter struct {
	text  string
	calls int
}

func (c *fixedChatter) Complete(_ context.Context, _ model.ChatRequest) (model.ChatResponse, error) {
	c.calls++
	return model.ChatResponse{Text: c.text, Model: "fake-chat", PromptTokens: 12, CompletionTokens: 5}, nil
}

func (c *fixedChatter) Name() string { return "fake-chat" }

// Ask is the full online path: authorization scope, retrieval, citation
// validation, answer persistence and resumable events. The test drives the real
// services with a fixed recall hit and generation adapter so a regression in
// any stage shows up here, not only in a single-layer unit test.
func TestAskEndToEndPersistsGroundedAnswer(t *testing.T) {
	service, db, chat := newAskFixture(t, "The answer is 42 [1].")
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "dev"}
	conversation, err := service.CreateConversation(context.Background(), principal, CreateConversationInput{KnowledgeBaseID: "kb-a", Title: "golden"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	var events []Event
	result, err := service.Ask(context.Background(), principal, AskInput{
		ConversationID: conversation.ID, Query: "What is the answer?", RequestID: "req-golden",
	}, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}

	if result.Status != AnswerCompleted {
		t.Fatalf("status = %s, want %s (code=%s message=%s)", result.Status, AnswerCompleted, result.ErrorCode, result.ErrorMessage)
	}
	if result.Content != "The answer is 42 [1]." || result.Model != "fake-chat" {
		t.Fatalf("unexpected answer: %+v", result)
	}
	if !result.Validation.Valid || len(result.Citations) != 1 || result.Citations[0].ChunkID != "chunk-1" {
		t.Fatalf("citation contract broken: %+v", result.Citations)
	}
	if len(events) < 2 || events[0].Sequence != 1 {
		t.Fatalf("events must start at sequence 1: %+v", events)
	}
	for index := 1; index < len(events); index++ {
		if events[index].Sequence != events[index-1].Sequence+1 {
			t.Fatalf("event sequence is not contiguous at %d: %+v", index, events)
		}
	}
	if events[len(events)-1].Type != EventComplete {
		t.Fatalf("terminal event = %s, want %s", events[len(events)-1].Type, EventComplete)
	}

	var assistant models.Message
	if err := db.Where("conversation_id = ? AND role = ?", conversation.ID, MessageRoleAssistant).First(&assistant).Error; err != nil {
		t.Fatalf("load assistant message: %v", err)
	}
	if assistant.Status != MessageStatusCompleted || assistant.Model != "fake-chat" || assistant.Content != result.Content {
		t.Fatalf("assistant message did not record the answer: %+v", assistant)
	}
	var citationCount int64
	if err := db.Model(&models.MessageCitation{}).Where("message_id = ?", assistant.ID).Count(&citationCount).Error; err != nil {
		t.Fatalf("count citations: %v", err)
	}
	if citationCount != 1 {
		t.Fatalf("persisted citations = %d, want 1", citationCount)
	}

	// A retried request id replays the recorded answer instead of spending a
	// second model call or appending duplicate events.
	replayed, err := service.Ask(context.Background(), principal, AskInput{
		ConversationID: conversation.ID, Query: "What is the answer?", RequestID: "req-golden",
	}, func(Event) error {
		t.Fatal("an idempotent replay must not re-emit events")
		return nil
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayed.MessageID != result.MessageID || chat.calls != 1 {
		t.Fatalf("replay must reuse the recorded answer: message=%s calls=%d", replayed.MessageID, chat.calls)
	}
}

func TestAskRejectsAnswerCitingUnregisteredChunk(t *testing.T) {
	service, db, _ := newAskFixture(t, "The answer is 42 [99].")
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "dev"}
	conversation, err := service.CreateConversation(context.Background(), principal, CreateConversationInput{KnowledgeBaseID: "kb-a"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	result, err := service.Ask(context.Background(), principal, AskInput{
		ConversationID: conversation.ID, Query: "What is the answer?", RequestID: "req-invalid",
	}, func(Event) error { return nil })
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if result.Status != AnswerRefused || result.ErrorCode != CodeCitationRejected {
		t.Fatalf("invalid citation must be refused: status=%s code=%s", result.Status, result.ErrorCode)
	}
	if strings.Contains(result.Content, "[99]") {
		t.Fatalf("rejected answer text must not be delivered: %q", result.Content)
	}
	var assistant models.Message
	if err := db.Where("conversation_id = ? AND role = ?", conversation.ID, MessageRoleAssistant).First(&assistant).Error; err != nil {
		t.Fatalf("load assistant message: %v", err)
	}
	if assistant.Status != AnswerRefused || assistant.ErrorCode != CodeCitationRejected {
		t.Fatalf("refusal was not persisted: %+v", assistant)
	}
}

func newAskFixture(t *testing.T, answer string) (*Service, *gorm.DB, *fixedChatter) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.Membership{}, &models.AuthorizationRevision{}, &models.KnowledgeBase{},
		&models.KnowledgeBaseMember{}, &models.AuditLog{},
		&models.Conversation{}, &models.Message{}, &models.MessageCitation{}, &models.StreamEvent{},
	); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	if err := db.Create(&models.Membership{TenantID: "tenant-a", UserID: "user-a", Role: "owner"}).Error; err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if err := db.Create(&models.KnowledgeBase{
		ID: "kb-a", TenantID: "tenant-a", OwnerID: "user-a", Name: "KB A",
		RetrievalConfig: "{}", IndexingStrategy: "{}",
	}).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}
	authzService, err := authz.NewService(db)
	if err != nil {
		t.Fatalf("authz service: %v", err)
	}
	retrievalService := retrieval.NewService(retrieval.Options{
		DB:    db,
		Authz: authzService,
		Keyword: fixedKeywordSearcher{hits: []retrieval.Hit{{
			ChunkID: "chunk-1", DocumentID: "doc-1", DocumentVersionID: "ver-1", KnowledgeBaseID: "kb-a",
			Content: "The answer is 42.", Score: 0.9, Rank: 1,
		}}},
		Defaults: retrieval.Params{
			TopK: 5, EmbeddingTopK: 5, KeywordTopK: 5,
			ContextTokenLimit: 2000, RRFK: 60, ExpansionScoreFactor: 0.5,
		},
	})
	promptRegistry, err := prompts.Load("../../config/prompt_templates")
	if err != nil {
		t.Fatalf("load prompts: %v", err)
	}
	chat := &fixedChatter{text: answer}
	service := NewService(Options{
		DB: db, Authz: authzService, Retrieval: retrievalService,
		Chat: chat, Prompts: promptRegistry, Timeout: 5 * time.Second,
	})
	return service, db, chat
}
