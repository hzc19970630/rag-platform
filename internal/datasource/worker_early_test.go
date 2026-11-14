package datasource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/secrets"
	"gorm.io/gorm"
)

type dsObjects struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (o *dsObjects) Put(_ context.Context, key string, content []byte, _ string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.data[key] = append([]byte(nil), content...)
	return nil
}

func (o *dsObjects) Get(_ context.Context, key string) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	content, ok := o.data[key]
	if !ok {
		return nil, errors.New("object not found")
	}
	return content, nil
}

func (o *dsObjects) Stat(_ context.Context, key string) (int64, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	content, ok := o.data[key]
	if !ok {
		return 0, errors.New("object not found")
	}
	return int64(len(content)), nil
}

func (o *dsObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	content, err := o.Get(context.Background(), key)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (o *dsObjects) Delete(_ context.Context, key string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.data, key)
	return nil
}

func seedWorkerConnector(t *testing.T, db *gorm.DB, id, manifestURL, connectorType, credentialRef string, enabled bool) {
	t.Helper()
	now := time.Now()
	config := ConnectorConfig{ManifestURL: manifestURL, MaxItemsPerRun: 500, ACLMode: ACLModeKnowledgeBase}
	encoded, _ := json.Marshal(config)
	if err := db.Create(&models.Connector{
		ID: id, TenantID: "tenant-a", KnowledgeBaseID: "kb-a", Name: id, Type: connectorType,
		Config: string(encoded), CredentialRef: credentialRef, Enabled: enabled,
		CreatedBy: "owner-a", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	if !enabled {
		if err := db.Model(&models.Connector{}).Where("id = ?", id).Update("enabled", false).Error; err != nil {
			t.Fatalf("disable connector: %v", err)
		}
	}
}

func seedWorkerRun(t *testing.T, db *gorm.DB, runID, connectorID, status string) SyncTask {
	t.Helper()
	now := time.Now()
	if err := db.Create(&models.SyncRun{
		ID: runID, TenantID: "tenant-a", ConnectorID: connectorID, KnowledgeBaseID: "kb-a",
		Mode: ModeManual, Status: status, Cursor: "{}", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return SyncTask{TenantID: "tenant-a", ConnectorID: connectorID, SyncRunID: runID}
}

func loadRun(t *testing.T, db *gorm.DB, runID string) models.SyncRun {
	t.Helper()
	var run models.SyncRun
	if err := db.First(&run, "id = ?", runID).Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	return run
}

func TestWorkerRejectsInvalidOrMissingWork(t *testing.T) {
	service, db := newDSFixture(t)
	ctx := context.Background()
	if err := service.ProcessSync(ctx, SyncTask{}); err == nil || !strings.Contains(err.Error(), "missing its run identity") {
		t.Fatalf("empty task = %v", err)
	}
	if err := service.ProcessSync(ctx, SyncTask{SyncRunID: "missing", TenantID: "tenant-a", ConnectorID: "missing"}); err != nil {
		t.Fatalf("missing run must be a no-op: %v", err)
	}
	seedWorkerConnector(t, db, "conn-1", "https://example.com/m.json", TypeHTTPManifest, "", true)
	task := seedWorkerRun(t, db, "run-done", "conn-1", StatusSucceeded)
	if err := service.ProcessSync(ctx, task); err != nil {
		t.Fatalf("inactive run must be a no-op: %v", err)
	}
	orphan := seedWorkerRun(t, db, "run-orphan", "missing-connector", StatusPending)
	if err := service.ProcessSync(ctx, orphan); err != nil {
		t.Fatalf("missing connector must be a no-op: %v", err)
	}
}

func TestWorkerTerminalAndCancellationBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("disabled connector", func(t *testing.T) {
		service, db := newDSFixture(t)
		seedWorkerConnector(t, db, "conn-disabled", "https://example.com/m.json", TypeHTTPManifest, "", false)
		task := seedWorkerRun(t, db, "run-disabled", "conn-disabled", StatusPending)
		if err := service.ProcessSync(ctx, task); err != nil {
			t.Fatalf("disabled connector = %v", err)
		}
		if run := loadRun(t, db, "run-disabled"); run.Status != StatusCancelled {
			t.Fatalf("disabled run = %+v", run)
		}
	})

	t.Run("invalid manifest URL", func(t *testing.T) {
		service, db := newDSFixture(t)
		seedWorkerConnector(t, db, "conn-bad-url", "ftp://example.com/m.json", TypeHTTPManifest, "", true)
		task := seedWorkerRun(t, db, "run-bad-url", "conn-bad-url", StatusPending)
		if err := service.ProcessSync(ctx, task); err != nil {
			t.Fatalf("invalid URL = %v", err)
		}
		run := loadRun(t, db, "run-bad-url")
		if run.Status != StatusFailed || !strings.Contains(run.ErrorSummary, "invalid connector config") {
			t.Fatalf("invalid URL run = %+v", run)
		}
	})

	t.Run("credential decrypt failure", func(t *testing.T) {
		service, db := newDSFixture(t)
		seedWorkerConnector(t, db, "conn-bad-cred", "https://example.com/m.json", TypeHTTPManifest, "v2:not-a-ciphertext", true)
		task := seedWorkerRun(t, db, "run-bad-cred", "conn-bad-cred", StatusPending)
		if err := service.ProcessSync(ctx, task); err != nil {
			t.Fatalf("bad credential = %v", err)
		}
		if run := loadRun(t, db, "run-bad-cred"); run.Status != StatusFailed {
			t.Fatalf("bad credential run = %+v", run)
		}
	})

	t.Run("unsupported connector type", func(t *testing.T) {
		service, db := newDSFixture(t)
		seedWorkerConnector(t, db, "conn-type", "https://example.com/m.json", "unknown_type", "", true)
		task := seedWorkerRun(t, db, "run-type", "conn-type", StatusPending)
		if err := service.ProcessSync(ctx, task); err != nil {
			t.Fatalf("unsupported type = %v", err)
		}
		run := loadRun(t, db, "run-type")
		if run.Status != StatusFailed || !strings.Contains(run.ErrorSummary, "unsupported connector type") {
			t.Fatalf("unsupported type run = %+v", run)
		}
	})

	t.Run("cancellation requested", func(t *testing.T) {
		service, db := newDSFixture(t)
		seedWorkerConnector(t, db, "conn-cancel", "https://example.com/m.json", TypeHTTPManifest, "", true)
		task := seedWorkerRun(t, db, "run-cancel", "conn-cancel", StatusPending)
		if err := db.Model(&models.SyncRun{}).Where("id = ?", "run-cancel").Update("cancel_requested", true).Error; err != nil {
			t.Fatalf("request cancellation: %v", err)
		}
		if err := service.ProcessSync(ctx, task); err != nil {
			t.Fatalf("cancelled run = %v", err)
		}
		if run := loadRun(t, db, "run-cancel"); run.Status != StatusCancelled || !run.CancelRequested {
			t.Fatalf("cancelled run = %+v", run)
		}
	})
}

func TestWorkerManifestFetchClassification(t *testing.T) {
	ctx := context.Background()
	newServer := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
	}

	t.Run("not found is terminal", func(t *testing.T) {
		service, db := newDSFixture(t)
		server := newServer(http.StatusNotFound)
		defer server.Close()
		seedWorkerConnector(t, db, "conn-404", server.URL+"/m.json", TypeHTTPManifest, "", true)
		task := seedWorkerRun(t, db, "run-404", "conn-404", StatusPending)
		if err := service.ProcessSync(ctx, task); err != nil {
			t.Fatalf("404 manifest = %v", err)
		}
		if run := loadRun(t, db, "run-404"); run.Status != StatusFailed {
			t.Fatalf("404 run = %+v", run)
		}
	})

	t.Run("server error is retryable", func(t *testing.T) {
		service, db := newDSFixture(t)
		server := newServer(http.StatusInternalServerError)
		defer server.Close()
		seedWorkerConnector(t, db, "conn-500", server.URL+"/m.json", TypeHTTPManifest, "", true)
		task := seedWorkerRun(t, db, "run-500", "conn-500", StatusPending)
		if err := service.ProcessSync(ctx, task); err == nil {
			t.Fatal("a 5xx manifest must surface a retryable error")
		}
		if run := loadRun(t, db, "run-500"); run.Status != StatusRunning {
			t.Fatalf("retryable run = %+v", run)
		}
	})
}

func TestWorkerSyncsAndTombstonesManifestItems(t *testing.T) {
	_, db := newDSFixture(t)
	ctx := context.Background()
	var manifestItems []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.json":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"items": manifestItems})
		case "/files/a.md":
			_, _ = writer.Write([]byte("hello from the connector"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	manifestItems = []map[string]string{{"id": "item-1", "url": server.URL + "/files/a.md", "path": "a.md", "version": "v1"}}

	cipher, err := secrets.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatalf("create cipher: %v", err)
	}
	authzService, err := authz.NewService(authz.NewPostgresStores(db))
	if err != nil {
		t.Fatalf("authz service: %v", err)
	}
	objects := &dsObjects{data: map[string][]byte{}}
	ingestionService := ingestion.NewService(db, authzService, objects, nil, 1<<20, 30, ingestion.Quota{})
	worker := NewWorker(db, cipher, NewFetcher(FetchOptions{AllowPrivateNetworks: true}), ingestionService)

	seedWorkerConnector(t, db, "conn-sync", server.URL+"/manifest.json", TypeHTTPManifest, "", true)
	first := seedWorkerRun(t, db, "run-sync-1", "conn-sync", StatusPending)
	if err := worker.Process(ctx, first); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	run := loadRun(t, db, "run-sync-1")
	if run.Status != StatusSucceeded || run.ItemsTotal != 1 || run.ItemsCreated != 1 || run.ItemsFailed != 0 {
		t.Fatalf("first run = %+v", run)
	}
	var item models.ConnectorItem
	if err := db.First(&item, "connector_id = ? AND external_id = ?", "conn-sync", "item-1").Error; err != nil {
		t.Fatalf("load connector item: %v", err)
	}
	if item.Status != ItemActive || item.DocumentID == nil {
		t.Fatalf("connector item = %+v", item)
	}
	var document models.Document
	if err := db.First(&document, "id = ?", *item.DocumentID).Error; err != nil {
		t.Fatalf("load ingested document: %v", err)
	}
	var connector models.Connector
	if err := db.First(&connector, "id = ?", "conn-sync").Error; err != nil {
		t.Fatalf("load connector: %v", err)
	}
	if connector.LastSyncAt == nil {
		t.Fatal("successful sync must record last_sync_at")
	}

	manifestItems = nil
	second := seedWorkerRun(t, db, "run-sync-2", "conn-sync", StatusPending)
	if err := worker.Process(ctx, second); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	run = loadRun(t, db, "run-sync-2")
	if run.Status != StatusSucceeded || run.ItemsDeleted != 1 {
		t.Fatalf("second run = %+v", run)
	}
	if err := db.First(&item, "connector_id = ? AND external_id = ?", "conn-sync", "item-1").Error; err != nil {
		t.Fatalf("reload connector item: %v", err)
	}
	if item.Status != ItemTombstoned || item.TombstonedAt == nil {
		t.Fatalf("tombstoned item = %+v", item)
	}
	if err := db.First(&document, "id = ?", *item.DocumentID).Error; err != nil {
		t.Fatalf("reload document: %v", err)
	}
	if document.DeletedAt == nil {
		t.Fatalf("document was not tombstoned: %+v", document)
	}
}

func TestWorkerRejectsOversizedManifest(t *testing.T) {
	service, db := newDSFixture(t)
	ctx := context.Background()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"items": []map[string]string{
			{"id": "a", "url": server.URL + "/a.md"},
			{"id": "b", "url": server.URL + "/b.md"},
		}})
	}))
	defer server.Close()
	now := time.Now()
	if err := db.Create(&models.Connector{
		ID: "conn-small", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", Name: "small", Type: TypeHTTPManifest,
		Config:  `{"manifestUrl":"` + server.URL + `/m.json","aclMode":"knowledge_base","maxItemsPerRun":1}`,
		Enabled: true, CreatedBy: "owner-a", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	task := seedWorkerRun(t, db, "run-small", "conn-small", StatusPending)
	if err := service.ProcessSync(ctx, task); err != nil {
		t.Fatalf("oversized manifest = %v", err)
	}
	run := loadRun(t, db, "run-small")
	if run.Status != StatusFailed || !strings.Contains(run.ErrorSummary, "above the configured limit") {
		t.Fatalf("oversized run = %+v", run)
	}
}
