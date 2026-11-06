//go:build nightly

package nightly_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/conversation"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Fifty concurrent replays exercise the pool and the per-request tenant
// transaction without an HTTP server. Each replay must return every event and
// release its connection.
func TestConcurrentReplayLoad(t *testing.T) {
	url := os.Getenv("TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("TEST_POSTGRES_URL is not set")
	}
	db, err := gorm.Open(postgres.Open(url), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(20)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := "nightly-" + suffix
	userID := "user-nightly-" + suffix
	knowledgeBaseID := "kb-nightly-" + suffix
	conversationID := "conv-nightly-" + suffix
	messageID := "msg-nightly-" + suffix
	requestID := "req-nightly-" + suffix
	t.Cleanup(func() {
		_ = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT set_config('app.tenant_id', ?, true)", tenantID).Error; err != nil {
				return err
			}
			_ = tx.Exec("DELETE FROM stream_events WHERE tenant_id = ?", tenantID).Error
			_ = tx.Exec("DELETE FROM messages WHERE tenant_id = ?", tenantID).Error
			_ = tx.Exec("DELETE FROM conversations WHERE tenant_id = ?", tenantID).Error
			_ = tx.Exec("DELETE FROM knowledge_bases WHERE tenant_id = ?", tenantID).Error
			_ = tx.Exec("DELETE FROM users WHERE id = ?", userID).Error
			return tx.Exec("DELETE FROM tenants WHERE id = ?", tenantID).Error
		})
	})
	const eventCount = 100
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.tenant_id', ?, true)", tenantID).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.Create(&models.Tenant{ID: tenantID, Name: "Nightly", Quota: "{}", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.User{ID: userID, Email: userID + "@example.test", DisplayName: "Nightly", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.KnowledgeBase{
			ID: knowledgeBaseID, TenantID: tenantID, OwnerID: userID, Name: "Nightly KB",
			RetrievalConfig: "{}", IndexingStrategy: "{}", CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.Conversation{
			ID: conversationID, TenantID: tenantID, KnowledgeBaseID: knowledgeBaseID,
			UserID: userID, Title: "Nightly", CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
		message := models.Message{
			ID: messageID, ConversationID: conversationID, TenantID: tenantID, KnowledgeBaseID: knowledgeBaseID,
			UserID: userID, RequestID: requestID, Role: conversation.MessageRoleUser, Content: "question",
			Status: conversation.MessageStatusCompleted, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		events := make([]models.StreamEvent, 0, eventCount)
		for index := 1; index <= eventCount; index++ {
			events = append(events, models.StreamEvent{
				RequestID: requestID, TenantID: tenantID, UserID: userID, Sequence: index,
				EventType: conversation.EventStatus, Payload: `{"state":"RETRIEVING"}`, CreatedAt: now,
			})
		}
		return tx.CreateInBatches(events, 50).Error
	}); err != nil {
		t.Fatalf("seed replay data: %v", err)
	}

	store := conversation.NewPostgresEventStore(db)
	const workers = 50
	durations := make([]time.Duration, workers)
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(slot int) {
			defer wait.Done()
			<-start
			began := time.Now()
			events, err := store.Replay(context.Background(), tenantID, userID, requestID, 0)
			if err != nil {
				errors <- err
				return
			}
			if len(events) != eventCount {
				errors <- fmt.Errorf("replayed %d events, want %d", len(events), eventCount)
				return
			}
			durations[slot] = time.Since(began)
		}(index)
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Fatalf("replay: %v", err)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[(len(durations)*95)/100-1]
	if p95 > 2*time.Second {
		t.Fatalf("replay p95 = %s, want <= 2s", p95)
	}
	t.Logf("replay p95 = %s for %d concurrent tails", p95, workers)
}
