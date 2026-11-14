package embeddingmigration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/embedding"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

type failingIndexer struct{ err error }

func (f failingIndexer) EnsureIndex(context.Context, int) error { return f.err }

type failingWriter struct{ err error }

func (f failingWriter) WriteVariants(context.Context, string, []VariantRecord) error { return f.err }

type failingPublisher struct{ err error }

func (f failingPublisher) EnqueueEmbeddingMigration(context.Context, string, string, int) error {
	return f.err
}

type customEmbedder struct {
	dimension   int
	vectorCount int
}

func (e customEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	count := e.vectorCount
	if count == 0 {
		count = len(texts)
	}
	vectors := make([][]float32, count)
	for i := range vectors {
		vectors[i] = make([]float32, e.dimension)
	}
	return vectors, nil
}
func (e customEmbedder) Dimension() int { return e.dimension }
func (e customEmbedder) Name() string   { return "target-embed" }

type customFactory struct {
	binding  embedding.Binding
	embedder embedding.Embedder
	err      error
}

func (f customFactory) EmbedderForModel(context.Context, string, string) (embedding.Binding, embedding.Embedder, error) {
	return f.binding, f.embedder, f.err
}

func newServiceWith(t *testing.T, db *gorm.DB, mutate func(*Options)) *Service {
	t.Helper()
	options := Options{
		DB: db, Authz: allowAuthz{allowed: true},
		Models:          fixedCatalog{binding: embedding.Binding{Model: "target-embed", Dimension: 8}},
		EmbedderFactory: testFactory{embedder: testEmbedder{name: "target-embed", dimension: 8}},
		Writer:          &dbWriter{db: db},
		Indexer:         &testIndexer{},
		Publisher:       &testPublisher{},
		BatchSize:       1,
	}
	if mutate != nil {
		mutate(&options)
	}
	service, err := NewService(options)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

func advanceToReady(t *testing.T, service *Service, jobID string) models.EmbeddingMigration {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := service.Step(ctx, jobID); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		job, err := service.load(ctx, jobID)
		if err != nil {
			t.Fatalf("load job: %v", err)
		}
		if job.Status == StatusReady {
			return job
		}
	}
	t.Fatal("migration did not reach ready")
	return models.EmbeddingMigration{}
}

func TestNewServiceValidatesOptions(t *testing.T) {
	if _, err := NewService(Options{}); err == nil || !strings.Contains(err.Error(), "requires a database") {
		t.Fatalf("missing database = %v", err)
	}
	db := openTestDB(t)
	if _, err := NewService(Options{DB: db}); err == nil || !strings.Contains(err.Error(), "requires authorization") {
		t.Fatalf("missing authz = %v", err)
	}
	if _, err := NewService(Options{DB: db, Authz: allowAuthz{}}); err == nil || !strings.Contains(err.Error(), "dependencies are incomplete") {
		t.Fatalf("missing dependencies = %v", err)
	}
	service := newServiceWith(t, db, func(options *Options) { options.BatchSize = 0 })
	if service.batchSize != defaultBatchSize {
		t.Fatalf("default batch size = %d, want %d", service.batchSize, defaultBatchSize)
	}
}

func TestStartValidationAndConflicts(t *testing.T) {
	db := openTestDB(t)
	seedMigrationFixture(t, db, 1)
	service := newServiceWith(t, db, nil)
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "owner", TenantID: "tenant-1"}

	if _, err := service.Start(ctx, principal, "missing-kb", "target-embed"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing knowledge base = %v", err)
	}

	empty := models.KnowledgeBase{ID: "kb-empty", TenantID: "tenant-1", OwnerID: "owner", Name: "Empty"}
	if err := db.Create(&empty).Error; err != nil {
		t.Fatalf("seed empty knowledge base: %v", err)
	}
	if _, err := service.Start(ctx, principal, "kb-empty", "target-embed"); err == nil {
		t.Fatal("knowledge base without a binding must be rejected")
	}

	alreadyBound := newServiceWith(t, db, func(options *Options) {
		options.Models = fixedCatalog{binding: embedding.Binding{Model: "source-embed", Dimension: 4}}
	})
	if _, err := alreadyBound.Start(ctx, principal, "kb-1", "source-embed"); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("already bound = %v", err)
	}

	catalogError := newServiceWith(t, db, func(options *Options) {
		options.Models = fixedCatalog{err: errors.New("catalog down")}
	})
	if _, err := catalogError.Start(ctx, principal, "kb-1", "target-embed"); err == nil || !strings.Contains(err.Error(), "catalog down") {
		t.Fatalf("catalog error = %v", err)
	}

	job, err := service.Start(ctx, principal, "kb-1", "target-embed")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if job.ID == "" {
		t.Fatal("start returned an empty id")
	}
	if _, err := service.Start(ctx, principal, "kb-1", "target-embed"); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("second start = %v, want conflict", err)
	}
}

func TestStartReportsPublisherFailure(t *testing.T) {
	db := openTestDB(t)
	seedMigrationFixture(t, db, 1)
	service := newServiceWith(t, db, func(options *Options) {
		options.Publisher = failingPublisher{err: errors.New("queue down")}
	})
	job, err := service.Start(context.Background(), authz.Principal{SubjectID: "owner", TenantID: "tenant-1"}, "kb-1", "target-embed")
	if err == nil || !strings.Contains(err.Error(), "enqueue migration") {
		t.Fatalf("publisher failure = %v", err)
	}
	if job.ID == "" {
		t.Fatal("start must return the created job alongside the enqueue error")
	}
}

func TestStepFailureAndTerminalPaths(t *testing.T) {
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "owner", TenantID: "tenant-1"}

	t.Run("unknown migration", func(t *testing.T) {
		db := openTestDB(t)
		service := newServiceWith(t, db, nil)
		if err := service.Step(ctx, "missing"); !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("Step = %v, want not found", err)
		}
	})

	t.Run("index build failure", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 1)
		service := newServiceWith(t, db, func(options *Options) {
			options.Indexer = failingIndexer{err: errors.New("index down")}
		})
		job, err := service.Start(ctx, principal, "kb-1", "target-embed")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := service.Step(ctx, job.ID); err == nil || !strings.Contains(err.Error(), "build target index") {
			t.Fatalf("Step = %v", err)
		}
	})

	t.Run("terminal state is a no-op", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 1)
		service := newServiceWith(t, db, nil)
		job, err := service.Start(ctx, principal, "kb-1", "target-embed")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := db.Model(&models.EmbeddingMigration{}).Where("id = ?", job.ID).Update("status", StatusCancelled).Error; err != nil {
			t.Fatalf("mark cancelled: %v", err)
		}
		if err := service.Step(ctx, job.ID); err != nil {
			t.Fatalf("terminal Step = %v", err)
		}
	})

	t.Run("factory binding mismatch", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 1)
		service := newServiceWith(t, db, func(options *Options) {
			options.EmbedderFactory = customFactory{
				binding:  embedding.Binding{Model: "target-embed", Dimension: 4},
				embedder: testEmbedder{name: "target-embed", dimension: 4},
			}
		})
		job, err := service.Start(ctx, principal, "kb-1", "target-embed")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := service.Step(ctx, job.ID); err != nil {
			t.Fatalf("build step: %v", err)
		}
		if err := service.Step(ctx, job.ID); err == nil || !strings.Contains(err.Error(), "migration expects 8 dimensions") {
			t.Fatalf("mismatched binding = %v", err)
		}
	})

	t.Run("vector dimension mismatch", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 1)
		service := newServiceWith(t, db, func(options *Options) {
			options.EmbedderFactory = customFactory{
				binding:  embedding.Binding{Model: "target-embed", Dimension: 8},
				embedder: customEmbedder{dimension: 4},
			}
		})
		job, err := service.Start(ctx, principal, "kb-1", "target-embed")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := service.Step(ctx, job.ID); err != nil {
			t.Fatalf("build step: %v", err)
		}
		if err := service.Step(ctx, job.ID); err == nil || !strings.Contains(err.Error(), "has dimension 4") {
			t.Fatalf("mismatched vector = %v", err)
		}
	})

	t.Run("vector count mismatch", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 2)
		service := newServiceWith(t, db, func(options *Options) {
			options.BatchSize = 2
			options.EmbedderFactory = customFactory{
				binding:  embedding.Binding{Model: "target-embed", Dimension: 8},
				embedder: customEmbedder{dimension: 8, vectorCount: 1},
			}
		})
		job, err := service.Start(ctx, principal, "kb-1", "target-embed")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := service.Step(ctx, job.ID); err != nil {
			t.Fatalf("build step: %v", err)
		}
		if err := service.Step(ctx, job.ID); err == nil || !strings.Contains(err.Error(), "returned 1 vectors") {
			t.Fatalf("mismatched vector count = %v", err)
		}
	})

	t.Run("writer failure leaves the cursor untouched", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 1)
		service := newServiceWith(t, db, func(options *Options) {
			options.Writer = failingWriter{err: errors.New("write down")}
		})
		job, err := service.Start(ctx, principal, "kb-1", "target-embed")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := service.Step(ctx, job.ID); err != nil {
			t.Fatalf("build step: %v", err)
		}
		if err := service.Step(ctx, job.ID); err == nil || !strings.Contains(err.Error(), "write down") {
			t.Fatalf("writer failure = %v", err)
		}
		stored, err := service.load(ctx, job.ID)
		if err != nil {
			t.Fatalf("load job: %v", err)
		}
		if stored.CursorChunkID != "" || stored.DoneChunks != 0 {
			t.Fatalf("cursor advanced after a failed write: %+v", stored)
		}
	})

	t.Run("verification returns to reembedding when rows are missing", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 1)
		service := newServiceWith(t, db, nil)
		job, err := service.Start(ctx, principal, "kb-1", "target-embed")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		advanceToReady(t, service, job.ID)
		if err := db.Where("model = ?", "target-embed").Delete(&models.ChunkEmbedding{}).Error; err != nil {
			t.Fatalf("remove target rows: %v", err)
		}
		if err := db.Model(&models.EmbeddingMigration{}).Where("id = ?", job.ID).Update("status", StatusVerifying).Error; err != nil {
			t.Fatalf("reset to verifying: %v", err)
		}
		if err := service.Step(ctx, job.ID); err != nil {
			t.Fatalf("verify step: %v", err)
		}
		stored, err := service.load(ctx, job.ID)
		if err != nil {
			t.Fatalf("load job: %v", err)
		}
		if stored.Status != StatusReembedding || stored.CursorChunkID != "" {
			t.Fatalf("job after missing chunks = %+v", stored)
		}
	})
}

func TestCutoverRollbackFinalizeAndCancelGuards(t *testing.T) {
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "owner", TenantID: "tenant-1"}

	t.Run("not ready cutover is rejected", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 1)
		service := newServiceWith(t, db, nil)
		pending := models.EmbeddingMigration{ID: "emig-pending", TenantID: "tenant-1", KnowledgeBaseID: "kb-1", Status: StatusPending, TargetModel: "target-embed", SourceModel: "source-embed"}
		if err := db.Create(&pending).Error; err != nil {
			t.Fatalf("seed pending job: %v", err)
		}
		if _, err := service.Cutover(ctx, principal, "kb-1", pending.ID); err == nil || !strings.Contains(err.Error(), "only a ready migration") {
			t.Fatalf("cutover pending = %v", err)
		}
		if _, err := service.Finalize(ctx, principal, "kb-1", pending.ID); err == nil || !strings.Contains(err.Error(), "only a cut-over migration") {
			t.Fatalf("finalize pending = %v", err)
		}
	})

	t.Run("wrong knowledge base is not found", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 1)
		service := newServiceWith(t, db, nil)
		job, err := service.Start(ctx, principal, "kb-1", "target-embed")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if _, err := service.Cutover(ctx, principal, "kb-other", job.ID); !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("cutover wrong kb = %v", err)
		}
		if _, err := service.Rollback(ctx, principal, "kb-other", job.ID); !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("rollback wrong kb = %v", err)
		}
		if _, err := service.Cancel(ctx, principal, "kb-other", job.ID); !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("cancel wrong kb = %v", err)
		}
	})

	t.Run("viewer cannot change the migration", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 1)
		service := newServiceWith(t, db, nil)
		job, err := service.Start(ctx, principal, "kb-1", "target-embed")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		viewer := newServiceWith(t, db, func(options *Options) {
			options.Authz = allowAuthz{allowed: false}
		})
		if _, err := viewer.Cutover(ctx, authz.Principal{SubjectID: "viewer", TenantID: "tenant-1"}, "kb-1", job.ID); !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("viewer cutover = %v, want forbidden", err)
		}
		if _, err := viewer.Latest(ctx, authz.Principal{SubjectID: "viewer", TenantID: "tenant-1"}, "kb-1"); !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("viewer latest = %v, want forbidden", err)
		}
	})

	t.Run("cutover finalize and cancel guards", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 1)
		service := newServiceWith(t, db, nil)
		job, err := service.Start(ctx, principal, "kb-1", "target-embed")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		advanceToReady(t, service, job.ID)
		if _, err := service.Cutover(ctx, principal, "kb-1", job.ID); err != nil {
			t.Fatalf("cutover: %v", err)
		}
		if _, err := service.Cutover(ctx, principal, "kb-1", job.ID); err == nil {
			t.Fatal("a second cutover must be rejected")
		}
		if _, err := service.Cancel(ctx, principal, "kb-1", job.ID); err == nil || !strings.Contains(err.Error(), "must be rolled back") {
			t.Fatalf("cancel after cutover = %v", err)
		}
		if _, err := service.Rollback(ctx, principal, "kb-1", job.ID); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if _, err := service.Finalize(ctx, principal, "kb-1", job.ID); err == nil {
			t.Fatal("finalize after rollback must be rejected")
		}
	})

	t.Run("finalize removes only the source space", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 1)
		service := newServiceWith(t, db, nil)
		job, err := service.Start(ctx, principal, "kb-1", "target-embed")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		advanceToReady(t, service, job.ID)
		now := time.Now().UTC()
		if err := db.Create(&models.ChunkEmbedding{ChunkID: "chunk-a", TenantID: "tenant-1", KnowledgeBaseID: "kb-1", Model: "source-embed", Dimension: 4, Embedding: "vec", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatalf("seed source embedding: %v", err)
		}
		if _, err := service.Cutover(ctx, principal, "kb-1", job.ID); err != nil {
			t.Fatalf("cutover: %v", err)
		}
		finalized, err := service.Finalize(ctx, principal, "kb-1", job.ID)
		if err != nil {
			t.Fatalf("finalize: %v", err)
		}
		if finalized.Status != StatusFinalized {
			t.Fatalf("finalized status = %s", finalized.Status)
		}
		var sourceCount, targetCount int64
		if err := db.Model(&models.ChunkEmbedding{}).Where("model = ?", "source-embed").Count(&sourceCount).Error; err != nil {
			t.Fatalf("count source rows: %v", err)
		}
		if err := db.Model(&models.ChunkEmbedding{}).Where("model = ?", "target-embed").Count(&targetCount).Error; err != nil {
			t.Fatalf("count target rows: %v", err)
		}
		if sourceCount != 0 || targetCount != 1 {
			t.Fatalf("after finalize source=%d target=%d", sourceCount, targetCount)
		}
	})

	t.Run("cancel is idempotent and preserves failed jobs", func(t *testing.T) {
		db := openTestDB(t)
		seedMigrationFixture(t, db, 1)
		service := newServiceWith(t, db, nil)
		job, err := service.Start(ctx, principal, "kb-1", "target-embed")
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if _, err := service.Cancel(ctx, principal, "kb-1", job.ID); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		if _, err := service.Cancel(ctx, principal, "kb-1", job.ID); err != nil {
			t.Fatalf("second cancel = %v", err)
		}
		if err := db.Model(&models.EmbeddingMigration{}).Where("id = ?", job.ID).Update("status", StatusFailed).Error; err != nil {
			t.Fatalf("mark failed: %v", err)
		}
		failed, err := service.Cancel(ctx, principal, "kb-1", job.ID)
		if err != nil {
			t.Fatalf("cancel failed job: %v", err)
		}
		if failed.Status != StatusFailed {
			t.Fatalf("failed job status changed to %s", failed.Status)
		}
	})
}

func TestLatestReportsMissingAndMostRecent(t *testing.T) {
	db := openTestDB(t)
	seedMigrationFixture(t, db, 1)
	service := newServiceWith(t, db, nil)
	ctx := context.Background()
	principal := authz.Principal{SubjectID: "owner", TenantID: "tenant-1"}
	if _, err := service.Latest(ctx, principal, "kb-1"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("latest without migration = %v", err)
	}
	job, err := service.Start(ctx, principal, "kb-1", "target-embed")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	latest, err := service.Latest(ctx, principal, "kb-1")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.ID != job.ID {
		t.Fatalf("latest id = %s, want %s", latest.ID, job.ID)
	}
}
