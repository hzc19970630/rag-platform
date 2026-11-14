package datasource

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/secrets"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newDSFixture(t *testing.T) (*Service, *gorm.DB) {
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
	if err := db.AutoMigrate(
		&models.Connector{}, &models.SyncRun{}, &models.ConnectorItem{}, &models.OutboxEvent{},
		&models.KnowledgeBase{}, &models.KnowledgeBaseMember{}, &models.Membership{},
		&models.AuthorizationRevision{}, &models.AuditLog{},
		&models.Document{}, &models.DocumentVersion{}, &models.PipelineRun{}, &models.StageRun{},
		&models.TaskInbox{}, &models.TaskDeadLetter{}, &models.Chunk{}, &models.EvidencePackage{},
	); err != nil {
		t.Fatalf("migrate datasource tables: %v", err)
	}
	// Production migrations add the composite uniqueness the item upsert relies
	// on; AutoMigrate cannot infer it from the model.
	if err := db.Exec("CREATE UNIQUE INDEX connector_items_connector_external ON connector_items (connector_id, external_id)").Error; err != nil {
		t.Fatalf("create connector item unique index: %v", err)
	}
	now := time.Now()
	if err := db.Create(&[]models.Membership{
		{TenantID: "tenant-a", UserID: "owner-a", Role: "owner", CreatedAt: now, UpdatedAt: now},
		{TenantID: "tenant-a", UserID: "viewer-a", Role: "viewer", CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	if err := db.Create(&models.KnowledgeBase{
		ID: "kb-a", TenantID: "tenant-a", OwnerID: "owner-a", Name: "KB A", ACLVersion: 1,
		RetrievalConfig: "{}", IndexingStrategy: "{}", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}
	if err := db.Create(&models.KnowledgeBaseMember{KnowledgeBaseID: "kb-a", UserID: "viewer-a", Role: "viewer"}).Error; err != nil {
		t.Fatalf("seed knowledge base member: %v", err)
	}
	cipher, err := secrets.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatalf("create cipher: %v", err)
	}
	authzService, err := authz.NewService(authz.NewPostgresStores(db))
	if err != nil {
		t.Fatalf("authz service: %v", err)
	}
	return NewService(db, authzService, cipher, nil, FetchOptions{AllowPrivateNetworks: true}, 500), db
}

func dsOwner() authz.Principal  { return authz.Principal{SubjectID: "owner-a", TenantID: "tenant-a"} }
func dsViewer() authz.Principal { return authz.Principal{SubjectID: "viewer-a", TenantID: "tenant-a"} }

func createTestConnector(t *testing.T, service *Service, name string) ConnectorView {
	t.Helper()
	view, err := service.Connectors().Create(context.Background(), dsOwner(), "kb-a", CreateConnectorInput{
		Name:                name,
		Config:              ConnectorConfig{ManifestURL: "https://example.com/manifest.json"},
		Credential:          &CredentialInput{Type: "bearer", Token: "secret-token"},
		SyncIntervalSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("Create connector: %v", err)
	}
	return view
}

func TestConnectorCreateValidation(t *testing.T) {
	service, _ := newDSFixture(t)
	ctx := context.Background()
	valid := CreateConnectorInput{Name: "conn", Config: ConnectorConfig{ManifestURL: "https://example.com/m.json"}}

	if _, err := service.Connectors().Create(ctx, dsViewer(), "kb-a", valid); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("viewer create = %v, want forbidden", err)
	}
	emptyName := valid
	emptyName.Name = "  "
	if _, err := service.Connectors().Create(ctx, dsOwner(), "kb-a", emptyName); err == nil {
		t.Fatal("empty name must be rejected")
	}
	longName := valid
	longName.Name = strings.Repeat("n", 256)
	if _, err := service.Connectors().Create(ctx, dsOwner(), "kb-a", longName); err == nil {
		t.Fatal("long name must be rejected")
	}
	badType := valid
	badType.Type = "ftp"
	if _, err := service.Connectors().Create(ctx, dsOwner(), "kb-a", badType); err == nil {
		t.Fatal("unsupported type must be rejected")
	}
	badConfig := valid
	badConfig.Config.ManifestURL = "ftp://example.com/m.json"
	if _, err := service.Connectors().Create(ctx, dsOwner(), "kb-a", badConfig); err == nil {
		t.Fatal("invalid manifest URL must be rejected")
	}
	badInterval := valid
	badInterval.SyncIntervalSeconds = -1
	if _, err := service.Connectors().Create(ctx, dsOwner(), "kb-a", badInterval); err == nil {
		t.Fatal("negative sync interval must be rejected")
	}
	badCredential := valid
	badCredential.Credential = &CredentialInput{Type: "bearer"}
	if _, err := service.Connectors().Create(ctx, dsOwner(), "kb-a", badCredential); err == nil {
		t.Fatal("incomplete credential must be rejected")
	}
}

func TestConnectorCreateListGetPermissionsDelete(t *testing.T) {
	service, db := newDSFixture(t)
	ctx := context.Background()
	view := createTestConnector(t, service, "conn-1")
	if view.Name != "conn-1" || !view.Enabled || view.CreatedBy != "owner-a" || view.NextSyncAt == nil {
		t.Fatalf("connector view = %+v", view)
	}
	if view.Config.MaxItemsPerRun != defaultMaxItemsPerRun || view.Config.ACLMode != ACLModeKnowledgeBase {
		t.Fatalf("normalized config = %+v", view.Config)
	}
	if !view.HasCredential {
		t.Fatal("connector must report a stored credential")
	}
	var stored models.Connector
	if err := db.First(&stored, "id = ?", view.ID).Error; err != nil {
		t.Fatalf("load connector: %v", err)
	}
	if !strings.HasPrefix(stored.CredentialRef, "v2:") || strings.Contains(stored.CredentialRef, "secret-token") {
		t.Fatalf("credential was not sealed: %q", stored.CredentialRef)
	}

	list, err := service.Connectors().List(ctx, dsOwner(), "kb-a")
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %d, %v", len(list), err)
	}
	if _, err := service.Connectors().List(ctx, authz.Principal{SubjectID: "ghost", TenantID: "tenant-a"}, "kb-a"); err == nil {
		t.Fatal("unauthorized List must fail")
	}
	if _, err := service.Connectors().Get(ctx, dsOwner(), view.ID); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := service.Connectors().Get(ctx, dsOwner(), "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing Get = %v, want not found", err)
	}

	permissions, err := service.Connectors().Permissions(ctx, dsOwner(), view.ID)
	if err != nil {
		t.Fatalf("Permissions: %v", err)
	}
	if permissions.ConnectorID != view.ID || len(permissions.Members) != 1 || permissions.Members[0].UserID != "viewer-a" {
		t.Fatalf("permissions = %+v", permissions)
	}

	if err := service.Connectors().Delete(ctx, dsViewer(), view.ID); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("viewer delete = %v, want forbidden", err)
	}
	now := time.Now()
	if err := db.Create(&models.SyncRun{
		ID: "run-active", TenantID: "tenant-a", ConnectorID: view.ID, KnowledgeBaseID: "kb-a",
		Mode: ModeManual, Status: StatusPending, Cursor: "{}", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed active run: %v", err)
	}
	if err := service.Connectors().Delete(ctx, dsOwner(), view.ID); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("delete with active run = %v, want conflict", err)
	}
	if err := db.Model(&models.SyncRun{}).Where("id = ?", "run-active").Update("status", StatusSucceeded).Error; err != nil {
		t.Fatalf("settle run: %v", err)
	}
	if err := service.Connectors().Delete(ctx, dsOwner(), view.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestConnectorUpdate(t *testing.T) {
	service, db := newDSFixture(t)
	ctx := context.Background()
	view := createTestConnector(t, service, "conn-update")

	if _, err := service.Connectors().Update(ctx, dsViewer(), view.ID, UpdateConnectorInput{}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("viewer update = %v, want forbidden", err)
	}
	emptyName := "  "
	if _, err := service.Connectors().Update(ctx, dsOwner(), view.ID, UpdateConnectorInput{Name: &emptyName}); err == nil {
		t.Fatal("empty name update must be rejected")
	}
	badConfig := ConnectorConfig{ManifestURL: "ftp://example.com/m.json"}
	if _, err := service.Connectors().Update(ctx, dsOwner(), view.ID, UpdateConnectorInput{Config: &badConfig}); err == nil {
		t.Fatal("invalid config update must be rejected")
	}
	badCredential := CredentialInput{Type: "bearer"}
	if _, err := service.Connectors().Update(ctx, dsOwner(), view.ID, UpdateConnectorInput{Credential: &badCredential}); err == nil {
		t.Fatal("incomplete credential update must be rejected")
	}
	badInterval := -1
	if _, err := service.Connectors().Update(ctx, dsOwner(), view.ID, UpdateConnectorInput{SyncIntervalSeconds: &badInterval}); err == nil {
		t.Fatal("negative interval update must be rejected")
	}

	name := "renamed"
	config := ConnectorConfig{ManifestURL: "https://example.com/v2.json"}
	enabled := false
	interval := 0
	updated, err := service.Connectors().Update(ctx, dsOwner(), view.ID, UpdateConnectorInput{
		Name: &name, Config: &config, Enabled: &enabled, SyncIntervalSeconds: &interval,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "renamed" || updated.Enabled || updated.NextSyncAt != nil || updated.Config.ManifestURL != "https://example.com/v2.json" {
		t.Fatalf("updated connector = %+v", updated)
	}
	var stored models.Connector
	if err := db.First(&stored, "id = ?", view.ID).Error; err != nil {
		t.Fatalf("load connector: %v", err)
	}
	if stored.Name != "renamed" || stored.Enabled {
		t.Fatalf("stored connector = %+v", stored)
	}
}

func TestSyncRunManagers(t *testing.T) {
	service, db := newDSFixture(t)
	ctx := context.Background()
	view := createTestConnector(t, service, "conn-runs")

	if _, err := service.Runs().TriggerSync(ctx, dsViewer(), view.ID); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("viewer trigger = %v, want forbidden", err)
	}
	run, err := service.Runs().TriggerSync(ctx, dsOwner(), view.ID)
	if err != nil {
		t.Fatalf("TriggerSync: %v", err)
	}
	if run.Status != StatusPending || run.Mode != ModeManual || run.TriggeredBy != "owner-a" {
		t.Fatalf("run = %+v", run)
	}
	var outbox int64
	if err := db.Model(&models.OutboxEvent{}).Where("aggregate_id = ?", view.ID).Count(&outbox).Error; err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if outbox == 0 {
		t.Fatal("trigger must emit an outbox event")
	}

	runs, err := service.Runs().ListRuns(ctx, dsOwner(), view.ID, 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %d, %v", len(runs), err)
	}
	if _, err := service.Runs().GetRun(ctx, dsOwner(), view.ID, "missing"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing GetRun = %v", err)
	}

	if _, err := service.Runs().RetryRun(ctx, dsOwner(), view.ID, run.ID); !errors.Is(err, apperror.ErrConflict) {
		t.Fatalf("retry pending = %v, want conflict", err)
	}
	if err := db.Model(&models.SyncRun{}).Where("id = ?", run.ID).Update("status", StatusFailed).Error; err != nil {
		t.Fatalf("fail run: %v", err)
	}
	retried, err := service.Runs().RetryRun(ctx, dsOwner(), view.ID, run.ID)
	if err != nil {
		t.Fatalf("RetryRun: %v", err)
	}
	if retried.Mode != ModeResume || retried.ResumeOfRunID != run.ID {
		t.Fatalf("retried run = %+v", retried)
	}

	cancelled, err := service.Runs().CancelRun(ctx, dsOwner(), view.ID, retried.ID)
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if cancelled.Status != StatusCancelled || !cancelled.CancelRequested {
		t.Fatalf("cancelled run = %+v", cancelled)
	}

	running := models.SyncRun{
		ID: "run-running", TenantID: "tenant-a", ConnectorID: view.ID, KnowledgeBaseID: "kb-a",
		Mode: ModeManual, Status: StatusRunning, Cursor: "{}", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.Create(&running).Error; err != nil {
		t.Fatalf("seed running run: %v", err)
	}
	flagged, err := service.Runs().CancelRun(ctx, dsOwner(), view.ID, running.ID)
	if err != nil {
		t.Fatalf("CancelRun running: %v", err)
	}
	if flagged.Status != StatusRunning || !flagged.CancelRequested {
		t.Fatalf("flagged run = %+v", flagged)
	}
	count, err := service.Runs().CancelConnector(ctx, dsOwner(), view.ID)
	if err != nil {
		t.Fatalf("CancelConnector: %v", err)
	}
	if count != 1 {
		t.Fatalf("CancelConnector count = %d, want 1 for a flagged running run", count)
	}

	pending := models.SyncRun{
		ID: "run-pending", TenantID: "tenant-a", ConnectorID: view.ID, KnowledgeBaseID: "kb-a",
		Mode: ModeScheduled, Status: StatusPending, Cursor: "{}", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("seed pending run: %v", err)
	}
	count, err = service.Runs().CancelKnowledgeBase(ctx, dsOwner(), "kb-a")
	if err != nil || count != 2 {
		t.Fatalf("CancelKnowledgeBase = %d, %v", count, err)
	}
	if _, err := service.Runs().CancelKnowledgeBase(ctx, dsViewer(), "kb-a"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("viewer CancelKnowledgeBase = %v, want forbidden", err)
	}
}

func TestMaintenance(t *testing.T) {
	service, db := newDSFixture(t)
	ctx := context.Background()
	now := time.Now()
	due := now.Add(-time.Minute)
	if err := db.Create(&models.Connector{
		ID: "conn-due", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", Name: "due", Type: TypeHTTPManifest,
		Config:  `{"manifestUrl":"https://example.com/m.json","aclMode":"knowledge_base","maxItemsPerRun":500}`,
		Enabled: true, SyncIntervalSeconds: 3600, NextSyncAt: &due, CreatedBy: "owner-a", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed due connector: %v", err)
	}
	triggered, err := service.Maintenance().ScheduleDue(ctx, 0)
	if err != nil || triggered != 1 {
		t.Fatalf("ScheduleDue = %d, %v", triggered, err)
	}
	var scheduled models.SyncRun
	if err := db.First(&scheduled, "connector_id = ?", "conn-due").Error; err != nil {
		t.Fatalf("load scheduled run: %v", err)
	}
	if scheduled.Mode != ModeScheduled || scheduled.TriggeredBy != "scheduler" {
		t.Fatalf("scheduled run = %+v", scheduled)
	}

	staleLease := now.Add(-10 * time.Minute)
	old := now.Add(-2 * time.Hour)
	if err := db.Create(&[]models.SyncRun{
		{ID: "run-stale", TenantID: "tenant-a", ConnectorID: "conn-due", KnowledgeBaseID: "kb-a", Mode: ModeManual, Status: StatusRunning, Cursor: "{}", LeaseUntil: &staleLease, CreatedAt: now, UpdatedAt: now},
		{ID: "run-old", TenantID: "tenant-a", ConnectorID: "conn-due", KnowledgeBaseID: "kb-a", Mode: ModeManual, Status: StatusPending, Cursor: "{}", CreatedAt: old, UpdatedAt: old},
	}).Error; err != nil {
		t.Fatalf("seed stale runs: %v", err)
	}
	expired, err := service.Maintenance().ExpireStaleRuns(ctx, now)
	if err != nil || expired != 2 {
		t.Fatalf("ExpireStaleRuns = %d, %v", expired, err)
	}
	if err := service.Maintenance().FailRun(ctx, SyncTask{}, nil); err != nil {
		t.Fatalf("FailRun without identity = %v", err)
	}
	if err := service.Maintenance().FailRun(ctx, SyncTask{TenantID: "tenant-a", SyncRunID: scheduled.ID}, errors.New("exhausted")); err != nil {
		t.Fatalf("FailRun: %v", err)
	}
	var failed models.SyncRun
	if err := db.First(&failed, "id = ?", scheduled.ID).Error; err != nil {
		t.Fatalf("load failed run: %v", err)
	}
	if failed.Status != StatusFailed || !strings.Contains(failed.ErrorSummary, "exhausted") {
		t.Fatalf("failed run = %+v", failed)
	}
}
