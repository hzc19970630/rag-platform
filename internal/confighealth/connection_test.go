package confighealth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
)

func TestTestConnectionRoutesToProviderOrDeployment(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	actor := modelregistry.Actor{ID: "user-a"}
	healthy := modelregistry.ProviderTestResult{Healthy: true}
	unhealthy := modelregistry.ProviderTestResult{Healthy: false, Error: "provider down"}

	service := NewService(Options{
		EmbeddingDimension: 8,
		Now:                func() time.Time { return now },
		ProbeProvider: func(context.Context, string, modelregistry.Actor, string) (modelregistry.ProviderTestResult, error) {
			return healthy, nil
		},
		ProbeDeployment: func(context.Context, model.Capability) (modelregistry.ProviderTestResult, error) {
			return healthy, nil
		},
	})
	result, err := service.TestConnection(context.Background(), "tenant-a", actor, "chat", "provider-1")
	if err != nil || !result.Healthy || !result.CheckedAt.Equal(now) {
		t.Fatalf("provider connection = %+v, %v", result, err)
	}

	noProvider := NewService(Options{Now: func() time.Time { return now }})
	if _, err := noProvider.TestConnection(context.Background(), "tenant-a", actor, "chat", "provider-1"); err == nil {
		t.Fatal("provider probe without a catalog must fail")
	}
	if _, err := service.TestConnection(context.Background(), "tenant-a", actor, "vision", ""); err == nil {
		t.Fatal("unknown capability must fail")
	}
	if _, err := noProvider.TestConnection(context.Background(), "tenant-a", actor, "chat", ""); err == nil || !strings.Contains(err.Error(), "deployment models are not available") {
		t.Fatalf("deployment probe without models = %v", err)
	}

	failing := NewService(Options{
		Now: func() time.Time { return now },
		ProbeProvider: func(context.Context, string, modelregistry.Actor, string) (modelregistry.ProviderTestResult, error) {
			return modelregistry.ProviderTestResult{}, errors.New("probe failed")
		},
		ProbeDeployment: func(context.Context, model.Capability) (modelregistry.ProviderTestResult, error) {
			return modelregistry.ProviderTestResult{}, errors.New("probe failed")
		},
	})
	if _, err := failing.TestConnection(context.Background(), "tenant-a", actor, "chat", "provider-1"); err == nil {
		t.Fatal("provider probe error must propagate")
	}
	if _, err := failing.TestConnection(context.Background(), "tenant-a", actor, "chat", ""); err == nil {
		t.Fatal("deployment probe error must propagate")
	}

	down := NewService(Options{
		Now: func() time.Time { return now },
		ProbeDeployment: func(context.Context, model.Capability) (modelregistry.ProviderTestResult, error) {
			return unhealthy, nil
		},
	})
	result, err = down.TestConnection(context.Background(), "tenant-a", actor, "chat", "")
	if err != nil || result.Healthy || result.Error != "provider down" {
		t.Fatalf("unhealthy connection = %+v, %v", result, err)
	}
}

func TestTestConnectionValidatesEmbeddingDimension(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	newService := func(embedder Embedder) *Service {
		return NewService(Options{
			EmbeddingDimension: 8,
			Embedder:           embedder,
			Now:                func() time.Time { return now },
			ProbeDeployment: func(context.Context, model.Capability) (modelregistry.ProviderTestResult, error) {
				return modelregistry.ProviderTestResult{Healthy: true}, nil
			},
		})
	}
	actor := modelregistry.Actor{ID: "user-a"}

	result, err := newService(nil).TestConnection(context.Background(), "tenant-a", actor, "embedding", "")
	if err != nil || result.Healthy || !strings.Contains(result.Error, "adapter is not available") {
		t.Fatalf("missing embedder = %+v, %v", result, err)
	}

	result, err = newService(fakeEmbedder{err: errors.New("model down")}).TestConnection(context.Background(), "tenant-a", actor, "embedding", "")
	if err != nil || result.Healthy || !strings.Contains(result.Error, "model down") {
		t.Fatalf("embedder error = %+v, %v", result, err)
	}

	result, err = newService(fakeEmbedder{dimension: 4}).TestConnection(context.Background(), "tenant-a", actor, "embedding", "")
	if err != nil || result.Healthy || !strings.Contains(result.Error, "returned dimension 4") || result.Dimension != 4 {
		t.Fatalf("dimension mismatch = %+v, %v", result, err)
	}

	result, err = newService(fakeEmbedder{dimension: 8}).TestConnection(context.Background(), "tenant-a", actor, "embedding", "")
	if err != nil || !result.Healthy || result.Dimension != 8 {
		t.Fatalf("matching dimension = %+v, %v", result, err)
	}

	if _, err := newService(nil).TestConnection(context.Background(), "tenant-a", actor, "", ""); err == nil {
		t.Fatal("empty capability must fail")
	} else if _, ok := apperror.AsValidation(err); !ok {
		t.Fatalf("empty capability error = %v, want validation", err)
	}
}
