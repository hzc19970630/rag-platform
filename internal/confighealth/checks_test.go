package confighealth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newCheckDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.Tenant{}, &models.KnowledgeBase{}); err != nil {
		t.Fatalf("migrate check tables: %v", err)
	}
	return db
}

func TestIndexCheckStatuses(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	if check := NewService(Options{}).indexCheck(ctx, now); check.Status != StatusUnknown {
		t.Fatalf("no db check = %+v", check)
	}
	inspectError := NewService(Options{
		DB: newCheckDB(t),
		CheckIndexes: func(context.Context) (IndexReport, error) {
			return IndexReport{}, errors.New("catalog down")
		},
	})
	if check := inspectError.indexCheck(ctx, now); check.Status != StatusUnknown || !strings.Contains(check.Detail, "catalog down") {
		t.Fatalf("inspect error = %+v", check)
	}
	empty := NewService(Options{
		DB: newCheckDB(t),
		CheckIndexes: func(context.Context) (IndexReport, error) {
			return IndexReport{}, nil
		},
	})
	if check := empty.indexCheck(ctx, now); check.Status != StatusWarning {
		t.Fatalf("empty indexes = %+v", check)
	}
	invalid := NewService(Options{
		DB: newCheckDB(t),
		CheckIndexes: func(context.Context) (IndexReport, error) {
			return IndexReport{Indexes: []IndexStatus{{Name: "idx", Valid: false}}}, nil
		},
	})
	if check := invalid.indexCheck(ctx, now); check.Status != StatusError || !strings.Contains(check.Detail, "idx") {
		t.Fatalf("invalid index = %+v", check)
	}
	healthy := NewService(Options{
		DB: newCheckDB(t),
		CheckIndexes: func(context.Context) (IndexReport, error) {
			return IndexReport{Indexes: []IndexStatus{{Name: "idx", Dimension: 8, Valid: true}}}, nil
		},
	})
	if check := healthy.indexCheck(ctx, now); check.Status != StatusOK {
		t.Fatalf("healthy index = %+v", check)
	}
}

func TestDimensionCheckWithTenantBindings(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	db := newCheckDB(t)
	if err := db.Create(&models.KnowledgeBase{ID: "kb-a", TenantID: "tenant-a", OwnerID: "user-a", Name: "A", EmbeddingDimension: 8}).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}
	indexed := func(dimension int) func(context.Context) (IndexReport, error) {
		return func(context.Context) (IndexReport, error) {
			return IndexReport{Indexes: []IndexStatus{{Name: "idx", Dimension: dimension, Valid: true}}}, nil
		}
	}

	service := NewService(Options{DB: db, EmbeddingDimension: 8, CheckIndexes: indexed(8)})
	if check := service.dimensionCheck(ctx, "tenant-a", now); check.Status != StatusOK {
		t.Fatalf("matching dimensions = %+v", check)
	}
	service = NewService(Options{DB: db, EmbeddingDimension: 8, CheckIndexes: indexed(4)})
	if check := service.dimensionCheck(ctx, "tenant-a", now); check.Status != StatusError || !strings.Contains(check.Detail, "8:kb-a") {
		t.Fatalf("missing dimension index = %+v", check)
	}
	service = NewService(Options{
		DB: db, EmbeddingDimension: 8,
		CheckIndexes: func(context.Context) (IndexReport, error) {
			return IndexReport{}, errors.New("index down")
		},
	})
	if check := service.dimensionCheck(ctx, "tenant-a", now); check.Status != StatusUnknown {
		t.Fatalf("index read error = %+v", check)
	}
}

func TestDimensionCheckFallsBackToServingIndex(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	db := newCheckDB(t)
	service := NewService(Options{
		DB: db, EmbeddingDimension: 8,
		IndexDimension: func(context.Context) (int, bool, error) { return 8, true, nil },
	})
	if check := service.dimensionCheck(ctx, "tenant-empty", now); check.Status != StatusOK {
		t.Fatalf("matching serving index = %+v", check)
	}
	service = NewService(Options{
		DB: db, EmbeddingDimension: 8,
		IndexDimension: func(context.Context) (int, bool, error) { return 4, true, nil },
	})
	if check := service.dimensionCheck(ctx, "tenant-empty", now); check.Status != StatusError {
		t.Fatalf("mismatched serving index = %+v", check)
	}
	service = NewService(Options{
		DB: db, EmbeddingDimension: 8,
		IndexDimension: func(context.Context) (int, bool, error) { return 0, false, nil },
	})
	if check := service.dimensionCheck(ctx, "tenant-empty", now); check.Status != StatusWarning {
		t.Fatalf("missing serving index = %+v", check)
	}
	service = NewService(Options{
		DB: db, EmbeddingDimension: 8,
		IndexDimension: func(context.Context) (int, bool, error) { return 0, false, errors.New("catalog down") },
	})
	if check := service.dimensionCheck(ctx, "tenant-empty", now); check.Status != StatusUnknown {
		t.Fatalf("serving index error = %+v", check)
	}
	if check := NewService(Options{}).dimensionCheck(ctx, "tenant-a", now); check.Status != StatusUnknown {
		t.Fatalf("dimension check without db = %+v", check)
	}
}

func TestStoredDriftCheck(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	db := newCheckDB(t)
	service := NewService(Options{DB: db})

	if check := service.storedDriftCheck(ctx, "", now); check.Status != StatusSkipped {
		t.Fatalf("empty tenant drift = %+v", check)
	}
	if err := db.Create(&models.Tenant{ID: "tenant-ok", Name: "OK", RetrievalConfig: "{}", ChunkingConfig: "{}"}).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if check := service.storedDriftCheck(ctx, "tenant-ok", now); check.Status != StatusOK {
		t.Fatalf("clean tenant drift = %+v", check)
	}
	if err := db.Create(&models.Tenant{ID: "tenant-bad", Name: "Bad", RetrievalConfig: "{not-json", ChunkingConfig: "{}"}).Error; err != nil {
		t.Fatalf("seed bad tenant: %v", err)
	}
	if check := service.storedDriftCheck(ctx, "tenant-bad", now); check.Status != StatusError || !strings.Contains(check.Detail, "tenant.retrieval") {
		t.Fatalf("bad tenant drift = %+v", check)
	}
	if err := db.Create(&models.KnowledgeBase{
		ID: "kb-bad", TenantID: "tenant-ok", OwnerID: "user-a", Name: "Bad", RetrievalConfig: "{not-json}",
	}).Error; err != nil {
		t.Fatalf("seed bad knowledge base: %v", err)
	}
	if check := service.storedDriftCheck(ctx, "tenant-ok", now); check.Status != StatusError || !strings.Contains(check.Detail, "knowledge_base.kb-bad") {
		t.Fatalf("bad knowledge base drift = %+v", check)
	}
}

func TestValidateStoredAndModelNames(t *testing.T) {
	if problems := validateStored("empty", "retrieval", "  "); len(problems) != 0 {
		t.Fatalf("empty value problems = %v", problems)
	}
	if problems := validateStored("empty", "retrieval", "{}"); len(problems) != 0 {
		t.Fatalf("{} problems = %v", problems)
	}
	if problems := validateStored("broken", "retrieval", "{bad"); len(problems) != 1 || !strings.Contains(problems[0], "broken") {
		t.Fatalf("broken value problems = %v", problems)
	}
	if names := modelNames(nil); len(names) != 0 {
		t.Fatalf("modelNames(nil) = %v", names)
	}
	names := modelNames([]modelregistry.HealthStatus{{Model: "chat-a"}, {Model: "chat-b"}})
	if len(names) != 2 || names[0] != "chat-a" || names[1] != "chat-b" {
		t.Fatalf("modelNames = %v", names)
	}
}
