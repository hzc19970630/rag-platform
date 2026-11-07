package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/conversation"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func tailFixture(t *testing.T) (*gorm.DB, *conversation.Service) {
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
	if err := db.AutoMigrate(&models.Message{}, &models.StreamEvent{}); err != nil {
		t.Fatalf("migrate replay tables: %v", err)
	}
	if err := db.Create(&models.Message{
		ID: "msg-tail", ConversationID: "conv-tail", TenantID: "tenant-a", UserID: "user-a",
		RequestID: "req-tail", Role: conversation.MessageRoleAssistant, Status: conversation.MessageStatusCompleted,
	}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	return db, conversation.NewService(conversation.Options{Stores: conversation.NewPostgresStores(db)})
}

func seededEvents(t *testing.T, db *gorm.DB) {
	t.Helper()
	events := []models.StreamEvent{
		{RequestID: "req-tail", TenantID: "tenant-a", UserID: "user-a", Sequence: 1, EventType: conversation.EventStatus, Payload: `{"state":"RECEIVED"}`},
		{RequestID: "req-tail", TenantID: "tenant-a", UserID: "user-a", Sequence: 2, EventType: conversation.EventAnswer, Payload: `{"content":"answer"}`},
		{RequestID: "req-tail", TenantID: "tenant-a", UserID: "user-a", Sequence: 3, EventType: conversation.EventComplete, Payload: `{"status":"COMPLETED"}`},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatalf("seed events: %v", err)
	}
}

func tailConfig() config.Config {
	return config.Config{Answer: config.AnswerConfig{PollIntervalMS: 5, TailTimeoutSec: 2}}
}

func TestTailAnswerStreamResumesAfterLastSequence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, service := tailFixture(t)
	seededEvents(t, db)
	recorder := newLockedRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	stream := newSSEStream(ctx)

	tailAnswerStream(context.Background(), service, authz.Principal{TenantID: "tenant-a", SubjectID: "user-a"}, "req-tail", 1, tailConfig(), stream)
	stream.finish()

	body := recorder.String()
	if strings.Contains(body, "id: req-tail:1") {
		t.Fatalf("resume must not re-emit the sequence the client already saw: %q", body)
	}
	for _, want := range []string{"id: req-tail:2", "id: req-tail:3", "event: complete"} {
		if !strings.Contains(body, want) {
			t.Fatalf("resume body is missing %q: %q", want, body)
		}
	}
	if strings.Contains(body, tailTimeoutCode) {
		t.Fatalf("a terminal event must stop the tail before the timeout: %q", body)
	}
}

func TestTailAnswerStreamEndsWithExplicitTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, service := tailFixture(t)
	recorder := newLockedRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	stream := newSSEStream(ctx)
	cfg := config.Config{Answer: config.AnswerConfig{PollIntervalMS: 5, TailTimeoutSec: 1}}

	started := time.Now()
	tailAnswerStream(context.Background(), service, authz.Principal{TenantID: "tenant-a", SubjectID: "user-a"}, "req-tail", 0, cfg, stream)
	stream.finish()
	elapsed := time.Since(started)

	if elapsed < time.Second || elapsed > 3*time.Second {
		t.Fatalf("tail timeout took %s, want about 1s", elapsed)
	}
	if !strings.Contains(recorder.String(), tailTimeoutCode) {
		t.Fatalf("tail must tell the client to reconnect: %q", recorder.String())
	}
}

func TestTwoConcurrentTailsBothReceiveTerminalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, service := tailFixture(t)
	seededEvents(t, db)
	cfg := tailConfig()

	var wait sync.WaitGroup
	bodies := make([]string, 2)
	for index := range bodies {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			recorder := newLockedRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			stream := newSSEStream(ctx)
			tailAnswerStream(context.Background(), service, authz.Principal{TenantID: "tenant-a", SubjectID: "user-a"}, "req-tail", 0, cfg, stream)
			stream.finish()
			bodies[index] = recorder.String()
		}(index)
	}
	wait.Wait()
	for index, body := range bodies {
		if !strings.Contains(body, "event: complete") {
			t.Fatalf("tail %d did not receive the terminal event: %q", index, body)
		}
	}
}

// A streaming tail can outlive the request by minutes. The request transaction
// must be committed before the tail starts, or the API pins one pooled
// connection per open stream and exhausts the pool under reconnects.
func TestRequestEventsCommitsTenantTxBeforeTail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, service := tailFixture(t)
	seededEvents(t, db)

	requestTx := db.Begin()
	if requestTx.Error != nil {
		t.Fatalf("begin request tx: %v", requestTx.Error)
	}

	recorder := newLockedRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/api/v1/requests/req-tail/events?stream=1", nil)
	c.Request = c.Request.WithContext(tenantdb.WithTx(c.Request.Context(), requestTx))
	c.Params = gin.Params{{Key: "requestID", Value: "req-tail"}}
	c.Set(accessContextKey, authz.AccessContext{
		Principal: authz.Principal{TenantID: "tenant-a", SubjectID: "user-a"},
	})

	handleRequestEvents(c, ConversationDeps{Conversation: service}, tailConfig())

	if tenantdb.TxFrom(c.Request.Context()) != nil {
		t.Fatal("the streaming tail still holds the request transaction")
	}
	body := recorder.String()
	for _, want := range []string{"id: req-tail:1", "id: req-tail:3", "event: complete"} {
		if !strings.Contains(body, want) {
			t.Fatalf("tail body is missing %q: %q", want, body)
		}
	}
}
