package confighealth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
)

type fakeEmbedder struct {
	dimension int
	err       error
}

func (f fakeEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	return [][]float32{make([]float32, f.dimension)}, nil
}
func (f fakeEmbedder) Dimension() int { return f.dimension }
func (f fakeEmbedder) Name() string   { return "fake-embedding" }

func TestHealthReportsStaleBackupAndExpiredLicense(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, "rag-20260101.dump")
	if err := os.WriteFile(backup, []byte("dump"), 0o644); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(backup, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	service := NewService(Options{
		AppEnv:            "production",
		BackupDir:         dir,
		BackupMaxAgeHours: 24,
		LicenseExpiresAt:  "2026-11-01",
		Now:               func() time.Time { return now },
	})
	report, err := service.Health(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	statuses := map[string]Check{}
	for _, check := range report.Checks {
		statuses[check.ID] = check
	}
	if statuses["backup.age"].Status != StatusWarning || statuses["backup.age"].Severity != SeverityHigh {
		t.Fatalf("backup check = %+v", statuses["backup.age"])
	}
	if statuses["license.expiry"].Status != StatusError || statuses["license.expiry"].Severity != SeverityHigh {
		t.Fatalf("license check = %+v", statuses["license.expiry"])
	}
	if report.Status != StatusError || report.HighRisk < 2 {
		t.Fatalf("report = %+v", report)
	}
}

func TestHealthReportsFreshBackupAndValidLicense(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, "rag-20261113.dump")
	if err := os.WriteFile(backup, []byte("dump"), 0o644); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	now := time.Now().UTC()
	service := NewService(Options{
		BackupDir:         dir,
		BackupMaxAgeHours: 24,
		LicenseExpiresAt:  now.Add(90 * 24 * time.Hour).Format(time.RFC3339),
		Now:               func() time.Time { return now },
	})
	report, err := service.Health(context.Background(), "")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	statuses := map[string]Check{}
	for _, check := range report.Checks {
		statuses[check.ID] = check
	}
	if statuses["backup.age"].Status != StatusOK {
		t.Fatalf("backup check = %+v", statuses["backup.age"])
	}
	if statuses["license.expiry"].Status != StatusOK {
		t.Fatalf("license check = %+v", statuses["license.expiry"])
	}
}

func TestHealthDetectsRetrievalFileDrift(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("retrieval:\n  top_k: 8\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	service := NewService(Options{
		RetrievalConfigFile: path, RetrievalFileHash: "stale-hash",
	})
	report, err := service.Health(context.Background(), "")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	for _, check := range report.Checks {
		if check.ID == "drift.retrieval_file" {
			if check.Status != StatusWarning || check.Severity != SeverityHigh {
				t.Fatalf("drift check = %+v", check)
			}
			return
		}
	}
	t.Fatal("drift check missing")
}

func TestTestConnectionEmbeddingDimension(t *testing.T) {
	service := NewService(Options{
		EmbeddingDimension: 4,
		Embedder:           fakeEmbedder{dimension: 4},
		ProbeDeployment: func(context.Context, model.Capability) (modelregistry.ProviderTestResult, error) {
			return modelregistry.ProviderTestResult{Healthy: true}, nil
		},
	})
	result, err := service.TestConnection(context.Background(), "tenant-1", modelregistry.Actor{}, "embedding", "")
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if !result.Healthy || result.Dimension != 4 {
		t.Fatalf("result = %+v", result)
	}
}

func TestTestConnectionRejectsUnknownCapability(t *testing.T) {
	service := NewService(Options{
		ProbeDeployment: func(context.Context, model.Capability) (modelregistry.ProviderTestResult, error) {
			return modelregistry.ProviderTestResult{Healthy: true}, nil
		},
	})
	if _, err := service.TestConnection(context.Background(), "tenant-1", modelregistry.Actor{}, "vision", ""); err == nil {
		t.Fatal("expected capability error")
	}
}
