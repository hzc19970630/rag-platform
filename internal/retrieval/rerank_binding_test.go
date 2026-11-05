package retrieval

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type recordingModelReranker struct {
	model string
}

func (r *recordingModelReranker) Name() string { return "recording" }

func (r *recordingModelReranker) Rerank(_ context.Context, _ string, candidates []Candidate) ([]Candidate, error) {
	return candidates, nil
}

func (r *recordingModelReranker) RerankWithModel(_ context.Context, model, _ string, candidates []Candidate) ([]Candidate, error) {
	r.model = model
	return candidates, nil
}

type staticRerankModel struct {
	model string
}

func (r staticRerankModel) RerankModel(_ context.Context, _, _ string) (string, error) {
	return r.model, nil
}

type emptyKeywordChannel struct{}

func (emptyKeywordChannel) Name() string { return ChannelKeyword }

func (emptyKeywordChannel) Recall(_ context.Context, _ RecallRequest, _ Params) ([]Hit, error) {
	return nil, nil
}

func TestRetrieveReranksWithTheKnowledgeBaseBinding(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "retrieval.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&models.KnowledgeBase{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	now := time.Now().UTC()
	knowledgeBase := models.KnowledgeBase{
		ID: "kb-1", TenantID: "tenant-1", OwnerID: "owner", Name: "kb-1", ACLVersion: 1,
		RetrievalConfig:  `{"rerank_enabled": true}`,
		IndexingStrategy: `{"keyword_enabled": true, "vector_enabled": false}`,
		RerankModel:      "kb-rerank",
		CreatedAt:        now, UpdatedAt: now,
	}
	if err := db.Create(&knowledgeBase).Error; err != nil {
		t.Fatalf("create knowledge base: %v", err)
	}

	reranker := &recordingModelReranker{}
	service := NewService(Options{
		DB: db, Reranker: reranker, RerankModels: staticRerankModel{model: "kb-rerank"},
		Channels: []RecallChannel{emptyKeywordChannel{}}, Defaults: baseParams(),
	})
	if _, err := service.Retrieve(context.Background(), authz.AuthorizationScope{
		TenantID: "tenant-1", KnowledgeBaseIDs: []string{"kb-1"}, ACLVersion: "1",
	}, QueryInput{RequestID: "req-1", Query: "question", KnowledgeBaseID: "kb-1"}); err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if reranker.model != "kb-rerank" {
		t.Fatalf("the reranker must receive the knowledge base binding, got %q", reranker.model)
	}
}
