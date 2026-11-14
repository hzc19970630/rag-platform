package tenantconfig

import (
	"context"
	"errors"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestTenantRerankEnabledParsesOverrides(t *testing.T) {
	cases := []struct {
		name     string
		config   string
		expected bool
	}{
		{"empty object", `{}`, false},
		{"enabled", `{"rerank_enabled":true}`, true},
		{"disabled", `{"rerank_enabled":false}`, false},
		{"invalid json", `{bad`, false},
		{"wrong type", `{"rerank_enabled":"yes"}`, false},
		{"missing key", `{"top_k":5}`, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := tenantRerankEnabled(models.Tenant{RetrievalConfig: testCase.config}); got != testCase.expected {
				t.Fatalf("tenantRerankEnabled(%s) = %v, want %v", testCase.config, got, testCase.expected)
			}
		})
	}
}

func TestTenantLookupsFallBackAndReportMissing(t *testing.T) {
	db := openTenantConfigDB(t)
	service := NewService(db, allowOperator{}, testDeployment())
	ctx := context.Background()

	if raw, err := service.TenantRetrievalOverride(ctx, ""); err != nil || raw != "" {
		t.Fatalf("empty tenant override = %q, %v", raw, err)
	}
	if raw, err := service.TenantRetrievalOverride(ctx, "missing-tenant"); err != nil || raw != "" {
		t.Fatalf("missing tenant override = %q, %v", raw, err)
	}
	policy, err := service.ChunkingForTenant(ctx, "missing-tenant")
	if err != nil || policy.Size != testDeployment().Chunk.Size {
		t.Fatalf("missing tenant chunking = %+v, %v", policy, err)
	}
	if _, err := service.Get(ctx, authz.Principal{SubjectID: "owner", TenantID: "missing-tenant"}); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing tenant Get = %v, want not found", err)
	}

	if err := db.Model(&models.Tenant{}).Where("id = ?", "tenant-1").Update("chunking_config", `{bad`).Error; err != nil {
		t.Fatalf("seed invalid chunking: %v", err)
	}
	if _, err := service.ChunkingForTenant(ctx, "tenant-1"); err == nil {
		t.Fatal("invalid tenant chunking JSON must fail")
	}
}
