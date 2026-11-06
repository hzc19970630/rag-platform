package archtest

import (
	"reflect"
	"testing"
)

const gormModelSource = `package models
type Chunk struct { ID string; TenantID string }
func (Chunk) TableName() string { return "chunks" }
type User struct { ID string }
func (User) TableName() string { return "users" }
type StageRun struct { ID string }
func (StageRun) TableName() string { return "stage_runs" }
`

func gormCheck(t *testing.T, source string) []GormTenancyViolation {
	t.Helper()
	violations, err := CheckGormTenancy([]TenancyFile{
		{Path: "internal/platform/persistence/models/models.go", Source: []byte(gormModelSource)},
		{Path: "internal/sample/query.go", Source: []byte(source)},
	})
	if err != nil {
		t.Fatalf("check gorm tenancy: %v", err)
	}
	return violations
}

func TestCheckGormTenancyRequiresScopeOnTenantTables(t *testing.T) {
	source := `package sample
import (
	"gorm.io/gorm"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
)
func load(db *gorm.DB, rows *[]models.Chunk) {
	db.Model(&models.Chunk{}).Where("status = ?", "READY").Find(rows)
	db.Model(&models.User{}).Find(rows)
	db.Model(&models.StageRun{}).Find(rows)
	db.Model(&models.Chunk{}).Where("id = ?", "c1").First(rows)
	db.Model(&models.Chunk{}).Where("chunk_id = ?", "c1").Find(rows)
	tenantdb.MustScoped(db, "t1").Model(&models.Chunk{}).Find(rows)
	db.Model(&models.Chunk{}).Where("tenant_id = ?", "t1").Find(rows)
	_ = db.Model(&models.Chunk{}).Where("status = ?", "READY")
}`
	violations := gormCheck(t, source)
	if len(violations) != 2 {
		t.Fatalf("expected two unscoped collection reads, got %#v", violations)
	}
	for _, violation := range violations {
		if violation.Table != "chunks" || violation.Line == 0 {
			t.Fatalf("unexpected violation: %#v", violation)
		}
	}
}

func TestCheckGormTenancyAcceptsTableChainsAndTenantCreates(t *testing.T) {
	source := `package sample
import (
	"gorm.io/gorm"
	"github.com/example/rag-platform/internal/platform/persistence/models"
)
func load(db *gorm.DB, rows *[]models.Chunk) {
	db.Table("chunks").Where("tenant_id = ?", "t1").Find(rows)
	db.Table("chunks").Find(rows)
	db.Create(&models.Chunk{TenantID: "t1"})
}`
	violations := gormCheck(t, source)
	if len(violations) != 1 {
		t.Fatalf("expected only the unscoped table read, got %#v", violations)
	}
	if violations[0].Table != "chunks" {
		t.Fatalf("unexpected table: %#v", violations[0])
	}
}

func TestGormAllowlistIsExplicit(t *testing.T) {
	want := []GormAllowlistEntry{
		{
			File:   "internal/identity/service.go",
			Table:  "auth_sessions",
			Reason: "logout revokes by globally unique token id before any tenant context exists",
		},
		{
			File:   "internal/ingestion/rebuild.go",
			Table:  "chunks",
			Reason: "maintenance rebuild audit scans the whole corpus across tenants by design",
		},
		{
			File:   "internal/ingestion/rebuild.go",
			Table:  "knowledge_bases",
			Reason: "maintenance rebuild discovery lists knowledge bases needing a model/dimension rebuild",
		},
		{
			File:   "internal/pipeline/deadletter.go",
			Table:  "task_inbox",
			Reason: "inbox failure addresses one globally unique idempotency key",
		},
		{
			File:   "internal/pipeline/runtime.go",
			Table:  "task_inbox",
			Reason: "inbox lease transitions address one globally unique idempotency key",
		},
		{
			File:   "internal/platform/evidenceindex/postgres.go",
			Table:  "chunks",
			Reason: "embedding writer counts one document_version's chunks by globally unique version id",
		},
		{
			File:   "internal/platform/persistence/inboxlease/lease.go",
			Table:  "task_inbox",
			Reason: "lease maintenance resolves the tenant from the inbox row and addresses one idempotency key",
		},
	}
	if !reflect.DeepEqual(GormAllowlist, want) {
		t.Fatalf("GORM allowlist changed; update this test with the new file/table/reason:\nwant %#v\ngot  %#v", want, GormAllowlist)
	}
}
