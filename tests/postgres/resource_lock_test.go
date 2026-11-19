package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence"
	"gorm.io/gorm"
)

// The same key must serialize and time out with a conflict; a different key
// must not be blocked by the holder.
func TestResourceLockSerializesSameKeyAndTimesOut(t *testing.T) {
	db := openGuardDB(t)
	ctx := context.Background()

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.Transaction(func(tx *gorm.DB) error {
			if err := persistence.LockResourceXact(tx, "test:kb:lock", time.Second); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	err := db.Transaction(func(tx *gorm.DB) error {
		return persistence.LockResourceXact(tx, "test:kb:lock", 200*time.Millisecond)
	})
	if !errors.Is(err, apperror.ErrConflict) {
		close(release)
		<-done
		t.Fatalf("same-key lock error = %v, want ErrConflict", err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		return persistence.LockResourceXact(tx, "test:kb:other", 200*time.Millisecond)
	}); err != nil {
		close(release)
		<-done
		t.Fatalf("different key must not block: %v", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("holder transaction: %v", err)
	}
	_ = ctx
}
