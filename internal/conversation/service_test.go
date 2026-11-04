package conversation

import (
	"context"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLoadAnswerScopedToTenantUserAndConversation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.Message{}, &models.MessageCitation{}, &models.StreamEvent{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}

	messages := []models.Message{
		{ID: "msg-a", ConversationID: "conv-a", TenantID: "tenant-a", UserID: "user-a", RequestID: "req-shared", Role: MessageRoleAssistant, Content: "answer a", Status: MessageStatusCompleted},
		{ID: "msg-b", ConversationID: "conv-b", TenantID: "tenant-b", UserID: "user-b", RequestID: "req-shared", Role: MessageRoleAssistant, Content: "answer b", Status: MessageStatusCompleted},
		{ID: "msg-c", ConversationID: "conv-c", TenantID: "tenant-a", UserID: "user-c", RequestID: "req-shared", Role: MessageRoleUser, Content: "question c", Status: MessageStatusCompleted},
	}
	if err := db.Create(&messages).Error; err != nil {
		t.Fatalf("seed messages: %v", err)
	}

	service := NewService(Options{DB: db})
	ctx := context.Background()

	got, ok, err := service.loadAnswer(ctx, authz.Principal{TenantID: "tenant-a", SubjectID: "user-a"}, "conv-a", "req-shared")
	if err != nil || !ok {
		t.Fatalf("load tenant a answer: ok=%v err=%v", ok, err)
	}
	if got.Content != "answer a" {
		t.Fatalf("tenant a must not read another tenant's answer, got %q", got.Content)
	}

	got, ok, err = service.loadAnswer(ctx, authz.Principal{TenantID: "tenant-b", SubjectID: "user-b"}, "conv-b", "req-shared")
	if err != nil || !ok {
		t.Fatalf("load tenant b answer: ok=%v err=%v", ok, err)
	}
	if got.Content != "answer b" {
		t.Fatalf("tenant b must read its own answer, got %q", got.Content)
	}

	// A same-tenant user cannot reuse another user's conversation or request id.
	if _, ok, err := service.loadAnswer(ctx, authz.Principal{TenantID: "tenant-a", SubjectID: "user-c"}, "conv-a", "req-shared"); err != nil || ok {
		t.Fatalf("same tenant wrong user must not read the answer: ok=%v err=%v", ok, err)
	}
	if _, ok, err := service.loadAnswer(ctx, authz.Principal{TenantID: "tenant-a", SubjectID: "user-a"}, "conv-other", "req-shared"); err != nil || ok {
		t.Fatalf("wrong conversation must not read the answer: ok=%v err=%v", ok, err)
	}

	events := []models.StreamEvent{
		{RequestID: "req-shared", TenantID: "tenant-a", UserID: "user-a", Sequence: 1, EventType: EventStatus, Payload: `{"who":"a"}`},
		{RequestID: "req-shared", TenantID: "tenant-a", UserID: "user-c", Sequence: 1, EventType: EventStatus, Payload: `{"who":"c"}`},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatalf("seed stream events: %v", err)
	}
	replayed, err := service.ReplayEvents(ctx, authz.Principal{TenantID: "tenant-a", SubjectID: "user-a"}, "req-shared", 0)
	if err != nil {
		t.Fatalf("replay tenant a events: %v", err)
	}
	if len(replayed) != 1 || replayed[0].Data["who"] != "a" {
		t.Fatalf("replay must be scoped to the caller's user id, got %+v", replayed)
	}
}

func TestPurgeStreamEventsDeletesOnlyExpiredRowsInBatches(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.Message{}, &models.MessageCitation{}, &models.StreamEvent{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}
	now := time.Now().UTC()
	events := []models.StreamEvent{
		{RequestID: "old-1", TenantID: "tenant-a", UserID: "user-a", Sequence: 1, EventType: EventStatus, Payload: `{}`, CreatedAt: now.Add(-10 * 24 * time.Hour)},
		{RequestID: "old-2", TenantID: "tenant-a", UserID: "user-a", Sequence: 1, EventType: EventStatus, Payload: `{}`, CreatedAt: now.Add(-9 * 24 * time.Hour)},
		{RequestID: "old-3", TenantID: "tenant-a", UserID: "user-a", Sequence: 1, EventType: EventStatus, Payload: `{}`, CreatedAt: now.Add(-8 * 24 * time.Hour)},
		{RequestID: "new", TenantID: "tenant-a", UserID: "user-a", Sequence: 1, EventType: EventStatus, Payload: `{}`, CreatedAt: now.Add(-24 * time.Hour)},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatalf("seed stream events: %v", err)
	}

	service := NewService(Options{DB: db})
	ctx := context.Background()
	cutoff := now.Add(-7 * 24 * time.Hour)
	purged, err := service.PurgeStreamEvents(ctx, cutoff, 2)
	if err != nil {
		t.Fatalf("purge stream events: %v", err)
	}
	if purged != 2 {
		t.Fatalf("expected one batch of 2 rows, got %d", purged)
	}
	purged, err = service.PurgeStreamEvents(ctx, cutoff, 2)
	if err != nil {
		t.Fatalf("purge remaining stream events: %v", err)
	}
	if purged != 1 {
		t.Fatalf("expected the last expired row, got %d", purged)
	}
	var remaining []models.StreamEvent
	if err := db.Order("id").Find(&remaining).Error; err != nil {
		t.Fatalf("load remaining stream events: %v", err)
	}
	if len(remaining) != 1 || remaining[0].RequestID != "new" {
		t.Fatalf("only the non-expired replay buffer must remain, got %+v", remaining)
	}

	// The answer itself is stored on the message, so it outlives the replay buffer.
	messages := []models.Message{
		{ID: "msg-old", ConversationID: "conv-a", TenantID: "tenant-a", UserID: "user-a", RequestID: "old-1", Role: MessageRoleAssistant, Content: "answer survives", Status: MessageStatusCompleted},
	}
	if err := db.Create(&messages).Error; err != nil {
		t.Fatalf("seed messages: %v", err)
	}
	answer, ok, err := service.loadAnswer(ctx, authz.Principal{TenantID: "tenant-a", SubjectID: "user-a"}, "conv-a", "old-1")
	if err != nil || !ok || answer.Content != "answer survives" {
		t.Fatalf("answer must remain readable after its events are purged: ok=%v err=%v", ok, err)
	}
	replayed, err := service.ReplayEvents(ctx, authz.Principal{TenantID: "tenant-a", SubjectID: "user-a"}, "old-1", 0)
	if err != nil {
		t.Fatalf("replay expired request: %v", err)
	}
	if len(replayed) != 0 {
		t.Fatalf("expired request must replay no events, got %+v", replayed)
	}
}

// A dropped SSE client cancels the request context. The terminal answer and
// its replay events must still be durable, otherwise a reconnect through
// /requests/:id/events returns a stream that never terminates.
func TestFinishAnswerPersistsAfterClientCancellation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.Conversation{}, &models.Message{}, &models.MessageCitation{}, &models.StreamEvent{}); err != nil {
		t.Fatalf("migrate conversation tables: %v", err)
	}

	service := NewService(Options{DB: db})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sequence := 0
	emit := func(eventType string, data map[string]any) error {
		return service.appendEvent(ctx, "req-drop", "tenant-a", "user-a", &sequence, eventType, data, nil)
	}
	result, err := service.finishAnswer(ctx, emit, finishInput{
		conversation: models.Conversation{ID: "conv-a", TenantID: "tenant-a", UserID: "user-a", KnowledgeBaseID: "kb-a"},
		principal:    authz.Principal{TenantID: "tenant-a", SubjectID: "user-a"},
		requestID:    "req-drop",
		scope:        authz.AuthorizationScope{TenantID: "tenant-a", ACLVersion: "tenant-a:1"},
		query:        "question",
		started:      time.Now(),
		validation:   Validation{Valid: false, Reason: CodeClientDisconnected},
		content:      "cannot answer",
		status:       AnswerCancelled,
		code:         CodeClientDisconnected,
		model:        "test-model",
		message:      "client disconnected",
	})
	if err != nil {
		t.Fatalf("finishAnswer must persist after cancellation: %v", err)
	}
	if result.Status != AnswerCancelled {
		t.Fatalf("status = %q, want %q", result.Status, AnswerCancelled)
	}

	var message models.Message
	if err := db.First(&message, "request_id = ? AND role = ?", "req-drop", MessageRoleAssistant).Error; err != nil {
		t.Fatalf("terminal answer must be persisted: %v", err)
	}
	if message.Status != AnswerCancelled || message.ErrorCode != CodeClientDisconnected {
		t.Fatalf("persisted answer = %+v", message)
	}

	var events []models.StreamEvent
	if err := db.Where("request_id = ?", "req-drop").Order("sequence").Find(&events).Error; err != nil {
		t.Fatalf("load replay events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected answer and complete events, got %d", len(events))
	}
	if events[0].EventType != EventAnswer || events[1].EventType != EventComplete {
		t.Fatalf("unexpected replay events: %+v", events)
	}
}
