package archtest

import (
	"reflect"
	"testing"
)

func TestTenantTableCatalog(t *testing.T) {
	if len(TenantTables) != 33 {
		t.Fatalf("expected 33 tenant tables, got %d; update the RLS migration and this test together", len(TenantTables))
	}
	if len(tenantChildTables) != 4 {
		t.Fatalf("expected 4 child tables, got %d", len(tenantChildTables))
	}
}

func TestCheckTenancyDetectsUnscopedStatements(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		source     string
		wantCount  int
		wantTables []string
	}{
		{
			name: "raw select on a tenant table",
			source: `package sample
import "gorm.io/gorm"
func load(db *gorm.DB) { db.Raw("SELECT id FROM chunks").Rows() }`,
			wantCount:  1,
			wantTables: []string{"chunks"},
		},
		{
			name: "tenant predicate passes",
			source: `package sample
import "gorm.io/gorm"
func load(db *gorm.DB, tenantID string) {
	db.Raw("SELECT id FROM chunks WHERE tenant_id = ?", tenantID).Rows()
}`,
		},
		{
			name: "local variable loses the predicate",
			source: `package sample
import "gorm.io/gorm"
func load(db *gorm.DB) {
	query := "UPDATE chunks SET status = ? WHERE id = ?"
	db.Exec(query, "EMBEDDED", "chunk-1")
}`,
			wantCount:  1,
			wantTables: []string{"chunks"},
		},
		{
			name: "package constant",
			source: `package sample
import "gorm.io/gorm"
const documentsQuery = "SELECT id FROM documents"
func load(db *gorm.DB) { db.Raw(documentsQuery).Rows() }`,
			wantCount:  1,
			wantTables: []string{"documents"},
		},
		{
			name: "sql passed through a helper parameter",
			source: `package sample
import "gorm.io/gorm"
const neighbourQuery = "SELECT id FROM chunks"
func load(db *gorm.DB, query string) { db.Raw(query) }
func run(db *gorm.DB) { load(db, neighbourQuery) }`,
			wantCount:  1,
			wantTables: []string{"chunks"},
		},
		{
			name: "child table without its parent",
			source: `package sample
import "gorm.io/gorm"
func load(db *gorm.DB) { db.Raw("SELECT 1 FROM stage_runs WHERE status = ?", "PENDING").Rows() }`,
			wantCount:  1,
			wantTables: []string{"stage_runs"},
		},
		{
			name: "child table joined to its parent",
			source: `package sample
import "gorm.io/gorm"
func load(db *gorm.DB) {
	db.Raw("SELECT 1 FROM stage_runs sr JOIN pipeline_runs p ON p.id = sr.pipeline_run_id").Rows()
}`,
		},
		{
			name: "comment mentioning tenant_id does not count",
			source: `package sample
import "gorm.io/gorm"
func load(db *gorm.DB) { db.Raw("SELECT id FROM chunks -- tenant_id is added elsewhere").Rows() }`,
			wantCount:  1,
			wantTables: []string{"chunks"},
		},
		{
			name: "allowlisted retention sweep",
			path: "internal/authz/audit_store.go",
			source: `package authz
import "gorm.io/gorm"
func purge(db *gorm.DB) { db.Exec("DELETE FROM audit_logs WHERE created_at < ?", "2026-01-01") }`,
		},
		{
			name: "new table in an allowlisted file still fails",
			path: "internal/authz/audit_store.go",
			source: `package authz
import "gorm.io/gorm"
func purge(db *gorm.DB) { db.Exec("DELETE FROM chunks WHERE id = ?", "chunk-1") }`,
			wantCount:  1,
			wantTables: []string{"chunks"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := testCase.path
			if path == "" {
				path = "internal/sample/raw.go"
			}
			violations, err := CheckTenancy([]TenancyFile{{Path: path, Source: []byte(testCase.source)}})
			if err != nil {
				t.Fatalf("check tenancy: %v", err)
			}
			if len(violations) != testCase.wantCount {
				t.Fatalf("expected %d violations, got %#v", testCase.wantCount, violations)
			}
			if len(violations) == 0 {
				return
			}
			if !reflect.DeepEqual(violations[0].Tables, testCase.wantTables) {
				t.Fatalf("expected tables %v, got %v", testCase.wantTables, violations[0].Tables)
			}
		})
	}
}

func TestTenancyAllowlistIsExplicit(t *testing.T) {
	want := []TenancyAllowlistEntry{
		{
			File:   "internal/authz/audit_store.go",
			Tables: []string{"audit_logs"},
			Reason: "retention sweep: deletes one bounded batch of expired audit rows across all tenants",
		},
		{
			File:   "internal/conversation/event_store.go",
			Tables: []string{"stream_events"},
			Reason: "retention sweep: deletes one bounded batch of expired replay buffers across all tenants",
		},
		{
			File:   "internal/conversation/job_store.go",
			Tables: []string{"answer_jobs"},
			Reason: "stale-lease sweep: fails RUNNING jobs whose worker disappeared across all tenants; it reads tenant_id only to address the job's own terminal events",
		},
		{
			File:   "internal/platform/evidenceindex/postgres.go",
			Tables: []string{"chunks"},
			Reason: "embedding writer addresses one chunk by primary key (plus version id) from a pipeline task or the managed re-embed sweep; no list or scan is involved",
		},
		{
			File:   "internal/platform/projectioncache/postgres.go",
			Tables: []string{"evidence_packages", "projection_generation_cache"},
			Reason: "maintenance sweep: removes cache rows whose package is gone across all tenants; deleting a cache row only forces regeneration",
		},
	}
	if !reflect.DeepEqual(TenancyAllowlist, want) {
		t.Fatalf("tenancy allowlist changed; update this test with the new file/table/reason:\nwant %#v\ngot  %#v", want, TenancyAllowlist)
	}
}

func TestRepositoryRawSQLIsScoped(t *testing.T) {
	files, err := LoadTenancyFiles("../..", "internal", "cmd")
	if err != nil {
		t.Fatalf("load repository files: %v", err)
	}
	violations, err := CheckTenancy(files)
	if err != nil {
		t.Fatalf("check tenancy: %v", err)
	}
	for _, violation := range violations {
		t.Error(violation)
	}
}
