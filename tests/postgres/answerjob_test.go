package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/conversation"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

// withTenantTx runs fn inside a transaction with app.tenant_id set. answer_jobs
// has FORCE RLS, so a plain pooled statement cannot see its own rows.
func withTenantTx(t *testing.T, db *gorm.DB, tenantID string, fn func(tx *gorm.DB) error) {
	t.Helper()
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.tenant_id', ?, true)", tenantID).Error; err != nil {
			return err
		}
		return fn(tx)
	}); err != nil {
		t.Fatalf("tenant transaction: %v", err)
	}
}

func answerJobFixture(t *testing.T, db *gorm.DB, tenantID string) string {
	t.Helper()
	jobID := fmt.Sprintf("job-%d", time.Now().UnixNano())
	scope := "{}"
	withTenantTx(t, db, tenantID, func(tx *gorm.DB) error {
		store := conversation.NewPostgresAnswerJobStore(tx)
		_, created, err := store.CreateOrLoad(context.Background(), models.AnswerJob{
			ID: jobID, TenantID: tenantID, UserID: "user-a", ConversationID: "conv-a",
			RequestID: "req-" + jobID, Status: conversation.AnswerJobQueued, ScopeJSON: &scope,
		})
		if err != nil {
			return err
		}
		if !created {
			return fmt.Errorf("fixture job was not created")
		}
		return nil
	})
	t.Cleanup(func() {
		_ = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT set_config('app.tenant_id', ?, true)", tenantID).Error; err != nil {
				return err
			}
			return tx.Exec("DELETE FROM answer_jobs WHERE tenant_id = ?", tenantID).Error
		})
	})
	return jobID
}

func TestConcurrentAnswerJobClaimAdmitsOneWorker(t *testing.T) {
	db := openGuardDB(t)
	tenantID := fmt.Sprintf("answerjob-%d", time.Now().UnixNano())
	jobID := answerJobFixture(t, db, tenantID)

	const workers = 8
	start := make(chan struct{})
	results := make(chan bool, workers)
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec("SELECT set_config('app.tenant_id', ?, true)", tenantID).Error; err != nil {
					return err
				}
				store := conversation.NewPostgresAnswerJobStore(tx)
				_, claimed, err := store.Claim(context.Background(), jobID, tenantID, time.Now().Add(time.Minute), time.Now())
				if err != nil {
					return err
				}
				results <- claimed
				return nil
			})
			if err != nil {
				errors <- err
			}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatalf("claim: %v", err)
	}
	claimed := 0
	for ok := range results {
		if ok {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("concurrent claim admitted %d workers, want exactly 1", claimed)
	}

	var loaded models.AnswerJob
	withTenantTx(t, db, tenantID, func(tx *gorm.DB) error {
		var err error
		loaded, err = conversation.NewPostgresAnswerJobStore(tx).Load(context.Background(), jobID)
		return err
	})
	if loaded.Status != conversation.AnswerJobRunning || loaded.Attempts != 1 {
		t.Fatalf("claimed job = %+v, want RUNNING with one attempt", loaded)
	}
}

func TestStaleSweepAndReclaimAreMutuallyExclusive(t *testing.T) {
	db := openGuardDB(t)
	tenantID := fmt.Sprintf("answerjob-race-%d", time.Now().UnixNano())
	jobID := answerJobFixture(t, db, tenantID)

	withTenantTx(t, db, tenantID, func(tx *gorm.DB) error {
		store := conversation.NewPostgresAnswerJobStore(tx)
		_, claimed, err := store.Claim(context.Background(), jobID, tenantID, time.Now().Add(time.Minute), time.Now())
		if err != nil || !claimed {
			return fmt.Errorf("initial claim: claimed=%v err=%v", claimed, err)
		}
		return tx.Exec("UPDATE answer_jobs SET lease_until = ? WHERE id = ?", time.Now().Add(-time.Minute), jobID).Error
	})

	start := make(chan struct{})
	var swept bool
	var reclaimed bool
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT set_config('app.tenant_id', ?, true)", tenantID).Error; err != nil {
				return err
			}
			rows, err := conversation.NewPostgresAnswerJobStore(tx).SweepStale(context.Background(), time.Now(), 100)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.ID == jobID {
					swept = true
				}
			}
			return nil
		})
		if err != nil {
			t.Errorf("sweep: %v", err)
		}
	}()
	go func() {
		defer wait.Done()
		<-start
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT set_config('app.tenant_id', ?, true)", tenantID).Error; err != nil {
				return err
			}
			_, claimed, err := conversation.NewPostgresAnswerJobStore(tx).Claim(context.Background(), jobID, tenantID, time.Now().Add(time.Minute), time.Now())
			if err != nil {
				return err
			}
			reclaimed = claimed
			return nil
		})
		if err != nil {
			t.Errorf("reclaim: %v", err)
		}
	}()
	close(start)
	wait.Wait()

	if swept == reclaimed {
		t.Fatalf("sweep/reclaim must be mutually exclusive: swept=%v reclaimed=%v", swept, reclaimed)
	}
}
