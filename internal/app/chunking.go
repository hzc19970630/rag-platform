package app

import (
	"context"

	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/tenantconfig"
)

type tenantChunkingResolver struct {
	service *tenantconfig.Service
}

// TenantChunkingResolver adapts the tenant configuration service to the
// pipeline's per-document chunking policy port.
func TenantChunkingResolver(service *tenantconfig.Service) ingestion.ChunkingResolver {
	return tenantChunkingResolver{service: service}
}

func (r tenantChunkingResolver) ChunkerForTenant(ctx context.Context, tenantID string) (ingestion.Chunker, error) {
	policy, err := r.service.ChunkingForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return ingestion.NewRuneChunker(ingestion.RuneChunkerOptions{
		Size: policy.Size, Overlap: policy.Overlap, Markers: policy.Markers, MinSize: policy.MinSize,
	}), nil
}
