package httpapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/gin-gonic/gin"
)

// A single-connection pool with many concurrent tails is the worst case for the
// per-request tenant transaction. Every tail must return when its context is
// canceled instead of pinning the only connection or deadlocking.
func TestConcurrentAnswerTailsHonorCancellationOnSingleConnection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, service := tailFixture(t)
	seededEvents(t, db)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	const tails = 12
	cfg := config.Config{Answer: config.AnswerConfig{PollIntervalMS: 5, TailTimeoutSec: 600}}
	var wait sync.WaitGroup
	start := make(chan struct{})
	deadline := make(chan time.Duration, tails)
	for index := 0; index < tails; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			recorder := newLockedRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			stream := newSSEStream(ginContext)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			began := time.Now()
			tailAnswerStream(ctx, service, authz.Principal{TenantID: "tenant-a", SubjectID: "user-a"}, "req-tail", 3, cfg, stream)
			stream.finish()
			deadline <- time.Since(began)
		}()
	}
	close(start)
	wait.Wait()
	close(deadline)
	for elapsed := range deadline {
		if elapsed > 2*time.Second {
			t.Fatalf("a canceled tail kept the connection for %s", elapsed)
		}
	}
}
