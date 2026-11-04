package ingestion

import (
	"context"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/status"
	"gorm.io/gorm"
)

func seedReadyPackageMismatch(t *testing.T, db *gorm.DB, tenantID string) {
	t.Helper()
	now := time.Now()
	documentID := "doc-" + tenantID
	versionID := "ver-" + tenantID
	if err := db.Create(&models.Document{
		ID: documentID, TenantID: tenantID, KnowledgeBaseID: "kb-" + tenantID, OwnerID: "user-a",
		Filename: "doc.md", MimeType: "text/markdown", Status: StatusParsing,
		LatestVersionID: versionID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := db.Create(&models.DocumentVersion{
		ID: versionID, DocumentID: documentID, TenantID: tenantID, KnowledgeBaseID: "kb-" + tenantID,
		Version: 1, ContentHash: "hash", SizeBytes: 1, SourceObjectKey: "obj", Status: StatusParsing,
		ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if err := db.Create(&models.EvidencePackage{
		ID: "pkg-" + tenantID, TenantID: tenantID, KnowledgeBaseID: "kb-" + tenantID,
		DocumentVersionID: versionID, ContentHash: "hash", ACLVersion: 1,
		Status: status.EvidenceReady, PackageJSON: "{}", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed package: %v", err)
	}
}

func TestReconcileTenantRepairsOnlyItsTenant(t *testing.T) {
	db := newTestDB(t)
	seedReadyPackageMismatch(t, db, "tenant-a")
	seedReadyPackageMismatch(t, db, "tenant-b")

	service := &Service{db: db}
	report, err := service.ReconcileTenant(context.Background(), "tenant-a", time.Minute)
	if err != nil {
		t.Fatalf("reconcile tenant: %v", err)
	}
	if report.DocumentsRepaired != 1 || report.VersionsRepaired != 1 {
		t.Fatalf("expected one document and one version repaired, got %+v", report)
	}

	var document models.Document
	if err := db.First(&document, "id = ?", "doc-tenant-a").Error; err != nil {
		t.Fatalf("load tenant a document: %v", err)
	}
	if document.Status != StatusEvidenceReady {
		t.Fatalf("tenant a document status = %q", document.Status)
	}
	document = models.Document{}
	if err := db.First(&document, "id = ?", "doc-tenant-b").Error; err != nil {
		t.Fatalf("load tenant b document: %v", err)
	}
	if document.Status != StatusParsing {
		t.Fatalf("tenant b document was modified: %q", document.Status)
	}
}

func seedDeadLetter(t *testing.T, db *gorm.DB, tenantID string) {
	t.Helper()
	now := time.Now()
	key := tenantID + ":parse"
	if err := db.Create(&models.TaskInbox{
		IdempotencyKey: key, TaskType: TypeTask, Status: inboxSucceeded, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
	if err := db.Create(&models.TaskDeadLetter{
		IdempotencyKey: key, TenantID: tenantID, KnowledgeBaseID: "kb-" + tenantID,
		DocumentVersionID: "ver-" + tenantID, TaskType: TypeTask, Stage: StageParse,
		Attempts: 5, LastError: "failed", Payload: "{}", Status: deadLetterPending,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed dead letter: %v", err)
	}
}

func TestReconcileTenantResolvesOnlyItsDeadLetters(t *testing.T) {
	db := newTestDB(t)
	seedDeadLetter(t, db, "tenant-a")
	seedDeadLetter(t, db, "tenant-b")

	service := &Service{db: db}
	report, err := service.ReconcileTenant(context.Background(), "tenant-a", time.Minute)
	if err != nil {
		t.Fatalf("reconcile tenant: %v", err)
	}
	if report.DeadLettersResolved != 1 {
		t.Fatalf("expected one dead letter resolved, got %+v", report)
	}
	var letter models.TaskDeadLetter
	if err := db.First(&letter, "idempotency_key = ?", "tenant-a:parse").Error; err != nil {
		t.Fatalf("load tenant a dead letter: %v", err)
	}
	if letter.Status != deadLetterResolved {
		t.Fatalf("tenant a dead letter status = %q", letter.Status)
	}
	letter = models.TaskDeadLetter{}
	if err := db.First(&letter, "idempotency_key = ?", "tenant-b:parse").Error; err != nil {
		t.Fatalf("load tenant b dead letter: %v", err)
	}
	if letter.Status != deadLetterPending {
		t.Fatalf("tenant b dead letter was modified: %q", letter.Status)
	}
}

func seedStaleInbox(t *testing.T, db *gorm.DB, tenantID string, cutoff time.Time) {
	t.Helper()
	now := time.Now()
	pipelineID := "pipe-" + tenantID
	key := tenantID + ":parse"
	if err := db.Create(&models.PipelineRun{
		ID: pipelineID, PipelineType: PipelineTypeEvidence, TenantID: tenantID, KnowledgeBaseID: "kb-" + tenantID,
		DocumentID: "doc-" + tenantID, DocumentVersionID: "ver-" + tenantID, RequestID: "req", TraceID: "trace",
		IdempotencyKey: "pipeline-" + tenantID, Status: StatusEvidenceReady, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if err := db.Create(&models.StageRun{
		PipelineRunID: pipelineID, Stage: StageParse, Status: StageSucceeded,
		IdempotencyKey: key, CreatedAt: cutoff.Add(-time.Hour), UpdatedAt: cutoff.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	if err := db.Create(&models.TaskInbox{
		IdempotencyKey: key, TaskType: TypeTask, Status: inboxProcessing,
		LeaseOwner: "crashed-worker", LeaseUntil: timePointer(cutoff.Add(-time.Minute)),
		CreatedAt: cutoff.Add(-time.Hour), UpdatedAt: cutoff.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
}

func TestReconcileTenantExpiresOnlyItsInboxLeases(t *testing.T) {
	db := newTestDB(t)
	cutoff := time.Now().Add(-time.Minute)
	seedStaleInbox(t, db, "tenant-a", cutoff)
	seedStaleInbox(t, db, "tenant-b", cutoff)

	service := &Service{db: db}
	report, err := service.ReconcileTenant(context.Background(), "tenant-a", time.Minute)
	if err != nil {
		t.Fatalf("reconcile tenant: %v", err)
	}
	if report.InboxesExpired != 1 {
		t.Fatalf("expected one inbox expired, got %+v", report)
	}
	var inbox models.TaskInbox
	if err := db.First(&inbox, "idempotency_key = ?", "tenant-a:parse").Error; err != nil {
		t.Fatalf("load tenant a inbox: %v", err)
	}
	if inbox.Status != inboxFailed {
		t.Fatalf("tenant a inbox status = %q", inbox.Status)
	}
	if inbox.LeaseOwner != "" || inbox.LeaseUntil != nil {
		t.Fatalf("expired lease was not cleared: owner=%q until=%v", inbox.LeaseOwner, inbox.LeaseUntil)
	}
	inbox = models.TaskInbox{}
	if err := db.First(&inbox, "idempotency_key = ?", "tenant-b:parse").Error; err != nil {
		t.Fatalf("load tenant b inbox: %v", err)
	}
	if inbox.Status != inboxProcessing {
		t.Fatalf("tenant b inbox was modified: %q", inbox.Status)
	}
}

// A worker heartbeat keeps lease_until in the future even though updated_at is
// old. Reconcile must trust the lease, not the row age, or it kills live work.
func TestReconcileKeepsInboxWithLiveLease(t *testing.T) {
	db := newTestDB(t)
	cutoff := time.Now().Add(-time.Hour)
	seedStaleInbox(t, db, "tenant-a", cutoff)
	live := time.Now().Add(time.Hour)
	if err := db.Model(&models.TaskInbox{}).Where("idempotency_key = ?", "tenant-a:parse").
		UpdateColumn("lease_until", &live).Error; err != nil {
		t.Fatalf("renew lease: %v", err)
	}

	service := &Service{db: db}
	report, err := service.ReconcileTenant(context.Background(), "tenant-a", time.Minute)
	if err != nil {
		t.Fatalf("reconcile tenant: %v", err)
	}
	if report.InboxesExpired != 0 {
		t.Fatalf("a live lease was expired: %+v", report)
	}
	inbox := loadInbox(t, &PipelineWorker{db: db}, "tenant-a:parse")
	if inbox.Status != inboxProcessing {
		t.Fatalf("live inbox status = %q", inbox.Status)
	}
	if inbox.LeaseOwner != "crashed-worker" || inbox.LeaseUntil == nil || !inbox.LeaseUntil.After(time.Now()) {
		t.Fatalf("live lease was modified: owner=%q until=%v", inbox.LeaseOwner, inbox.LeaseUntil)
	}
}

func seedProjectionInbox(t *testing.T, db *gorm.DB, tenantID string, cutoff time.Time, status string) {
	t.Helper()
	now := time.Now()
	runID := "proj-" + tenantID
	key := runID + ":collect"
	if err := db.Create(&models.ProjectionRun{
		ID: runID, TenantID: tenantID, KnowledgeBaseID: "kb-" + tenantID, EvidencePackageID: "pkg-" + tenantID,
		ProjectionType: "wiki", Status: "DRAFT", SourceHash: "hash", ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed projection run: %v", err)
	}
	if err := db.Create(&models.ProjectionStageRun{
		ProjectionRunID: runID, ProjectionType: "wiki", Stage: "collect", Status: StageProcessing,
		IdempotencyKey: key, CreatedAt: cutoff.Add(-time.Hour), UpdatedAt: cutoff.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed projection stage: %v", err)
	}
	if err := db.Create(&models.TaskInbox{
		IdempotencyKey: key, TaskType: "projection.wiki.collect", Status: status,
		LeaseOwner: leaseOwnerFor(status), LeaseUntil: leaseUntilFor(status, cutoff),
		CreatedAt: cutoff.Add(-time.Hour), UpdatedAt: cutoff.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
}

func leaseOwnerFor(status string) string {
	if status == inboxProcessing {
		return "crashed-worker"
	}
	return ""
}

func leaseUntilFor(status string, cutoff time.Time) *time.Time {
	if status == inboxProcessing {
		return timePointer(cutoff.Add(-time.Minute))
	}
	return nil
}

func timePointer(value time.Time) *time.Time { return &value }

func TestReconcileTenantExpiresOnlyItsProjectionInboxLeases(t *testing.T) {
	db := newTestDB(t)
	cutoff := time.Now().Add(-time.Minute)
	seedProjectionInbox(t, db, "tenant-a", cutoff, inboxProcessing)
	seedProjectionInbox(t, db, "tenant-b", cutoff, inboxProcessing)

	service := &Service{db: db}
	report, err := service.ReconcileTenant(context.Background(), "tenant-a", time.Minute)
	if err != nil {
		t.Fatalf("reconcile tenant: %v", err)
	}
	if report.InboxesExpired != 1 {
		t.Fatalf("expected one projection inbox expired, got %+v", report)
	}
	var inbox models.TaskInbox
	if err := db.First(&inbox, "idempotency_key = ?", "proj-tenant-a:collect").Error; err != nil {
		t.Fatalf("load tenant a inbox: %v", err)
	}
	if inbox.Status != inboxFailed {
		t.Fatalf("tenant a inbox status = %q", inbox.Status)
	}
	if inbox.LeaseOwner != "" || inbox.LeaseUntil != nil {
		t.Fatalf("expired projection lease was not cleared: owner=%q until=%v", inbox.LeaseOwner, inbox.LeaseUntil)
	}
	inbox = models.TaskInbox{}
	if err := db.First(&inbox, "idempotency_key = ?", "proj-tenant-b:collect").Error; err != nil {
		t.Fatalf("load tenant b inbox: %v", err)
	}
	if inbox.Status != inboxProcessing {
		t.Fatalf("tenant b inbox was modified: %q", inbox.Status)
	}
}

func TestReconcileKeepsProjectionInboxWithLiveLease(t *testing.T) {
	db := newTestDB(t)
	cutoff := time.Now().Add(-time.Hour)
	seedProjectionInbox(t, db, "tenant-a", cutoff, inboxProcessing)
	live := time.Now().Add(time.Hour)
	if err := db.Model(&models.TaskInbox{}).Where("idempotency_key = ?", "proj-tenant-a:collect").
		UpdateColumn("lease_until", &live).Error; err != nil {
		t.Fatalf("renew lease: %v", err)
	}

	service := &Service{db: db}
	report, err := service.ReconcileTenant(context.Background(), "tenant-a", time.Minute)
	if err != nil {
		t.Fatalf("reconcile tenant: %v", err)
	}
	if report.InboxesExpired != 0 {
		t.Fatalf("a live projection lease was expired: %+v", report)
	}
	inbox := loadInbox(t, &PipelineWorker{db: db}, "proj-tenant-a:collect")
	if inbox.Status != inboxProcessing {
		t.Fatalf("live projection inbox status = %q", inbox.Status)
	}
}

// A projection inbox row of the tenant is visible through the projection run
// join; the expiry must still respect the lease status and cutoff. Without
// parentheses around the tenant OR-clause, the projection branch would match
// any row and turn a completed inbox back to FAILED.
func TestReconcileTenantKeepsCompletedProjectionInbox(t *testing.T) {
	db := newTestDB(t)
	cutoff := time.Now().Add(-time.Minute)
	seedProjectionInbox(t, db, "tenant-a", cutoff, inboxSucceeded)

	service := &Service{db: db}
	report, err := service.ReconcileTenant(context.Background(), "tenant-a", time.Minute)
	if err != nil {
		t.Fatalf("reconcile tenant: %v", err)
	}
	if report.InboxesExpired != 0 {
		t.Fatalf("completed inbox was expired: %+v", report)
	}
	var inbox models.TaskInbox
	if err := db.First(&inbox, "idempotency_key = ?", "proj-tenant-a:collect").Error; err != nil {
		t.Fatalf("load inbox: %v", err)
	}
	if inbox.Status != inboxSucceeded {
		t.Fatalf("completed inbox status changed to %q", inbox.Status)
	}
}

func seedStaleStage(t *testing.T, db *gorm.DB, tenantID string, cutoff time.Time) {
	t.Helper()
	now := time.Now()
	documentID := "doc-" + tenantID
	versionID := "ver-" + tenantID
	if err := db.Create(&models.Document{
		ID: documentID, TenantID: tenantID, KnowledgeBaseID: "kb-" + tenantID, OwnerID: "user-a",
		Filename: "doc.md", MimeType: "text/markdown", Status: StatusParsing,
		LatestVersionID: versionID, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := db.Create(&models.DocumentVersion{
		ID: versionID, DocumentID: documentID, TenantID: tenantID, KnowledgeBaseID: "kb-" + tenantID,
		Version: 1, ContentHash: "hash", SizeBytes: 1, SourceObjectKey: "obj", Status: StatusParsing,
		ACLVersion: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if err := db.Create(&models.PipelineRun{
		ID: "pipe-" + tenantID, PipelineType: PipelineTypeEvidence, TenantID: tenantID, KnowledgeBaseID: "kb-" + tenantID,
		DocumentID: documentID, DocumentVersionID: versionID, RequestID: "req", TraceID: "trace",
		IdempotencyKey: "pipeline-" + tenantID, Status: StatusParsing, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if err := db.Create(&models.StageRun{
		PipelineRunID: "pipe-" + tenantID, Stage: StageParse, Status: StagePending,
		IdempotencyKey: tenantID + ":parse", CreatedAt: cutoff.Add(-time.Hour), UpdatedAt: cutoff.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed stage: %v", err)
	}
}

func TestReconcileTenantRequeuesOnlyItsStages(t *testing.T) {
	db := newTestDB(t)
	cutoff := time.Now().Add(-time.Minute)
	seedStaleStage(t, db, "tenant-a", cutoff)
	seedStaleStage(t, db, "tenant-b", cutoff)

	service := &Service{db: db}
	report, err := service.ReconcileTenant(context.Background(), "tenant-a", time.Minute)
	if err != nil {
		t.Fatalf("reconcile tenant: %v", err)
	}
	if report.StagesRequeued != 1 {
		t.Fatalf("expected one stage requeued, got %+v", report)
	}
	var count int64
	if err := db.Model(&models.OutboxEvent{}).Where("tenant_id = ?", "tenant-a").Count(&count).Error; err != nil {
		t.Fatalf("count tenant a outbox: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one tenant a outbox event, got %d", count)
	}
	count = 0
	if err := db.Model(&models.OutboxEvent{}).Where("tenant_id = ?", "tenant-b").Count(&count).Error; err != nil {
		t.Fatalf("count tenant b outbox: %v", err)
	}
	if count != 0 {
		t.Fatalf("tenant b was requeued: %d outbox events", count)
	}
}
