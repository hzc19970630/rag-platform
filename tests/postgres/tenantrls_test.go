// Postgres-gated RLS tests. They need a database user that can create roles
// and tables (the compose owner role is a superuser). Run with
// TEST_POSTGRES_URL set; skipped otherwise.
package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func openRLSConn(t *testing.T) *sql.Conn {
	t.Helper()
	url := os.Getenv("TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("TEST_POSTGRES_URL is not set")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatalf("acquire postgres connection: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = db.Close()
	})
	return conn
}

// rlsFixture is a parent/child pair plus a dedicated role without BYPASSRLS.
// Everything is dropped by the returned cleanup, which runs after ROLLBACK so
// it never executes under the restricted role.
type rlsFixture struct {
	parent string
	child  string
	role   string
}

func setupRLSFixture(t *testing.T, conn *sql.Conn) rlsFixture {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	f := rlsFixture{
		parent: "rls_parent_" + suffix,
		child:  "rls_child_" + suffix,
		role:   "rls_app_" + suffix,
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(context.Background(), query, args...); err != nil {
			t.Fatalf("fixture setup %q: %v", query, err)
		}
	}
	exec(fmt.Sprintf("CREATE ROLE %s NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS", f.role))
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		_, _ = conn.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s, %s", f.child, f.parent))
		_, _ = conn.ExecContext(ctx, fmt.Sprintf("DROP ROLE IF EXISTS %s", f.role))
	})
	exec(fmt.Sprintf("CREATE TABLE %s (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL)", f.parent))
	exec(fmt.Sprintf("CREATE TABLE %s (id TEXT PRIMARY KEY, parent_id TEXT NOT NULL REFERENCES %s(id))", f.child, f.parent))
	exec(fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY", f.parent))
	exec(fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY", f.parent))
	exec(fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY", f.child))
	exec(fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY", f.child))
	exec(fmt.Sprintf("CREATE POLICY strict_tenant ON %s USING (tenant_id = current_setting('app.tenant_id', true)) WITH CHECK (tenant_id = current_setting('app.tenant_id', true))", f.parent))
	exec(fmt.Sprintf("CREATE POLICY strict_tenant ON %s USING (EXISTS (SELECT 1 FROM %s p WHERE p.id = parent_id AND p.tenant_id = current_setting('app.tenant_id', true))) WITH CHECK (EXISTS (SELECT 1 FROM %s p WHERE p.id = parent_id AND p.tenant_id = current_setting('app.tenant_id', true)))", f.child, f.parent, f.parent))
	exec(fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON %s, %s TO %s", f.parent, f.child, f.role))
	exec(fmt.Sprintf("INSERT INTO %s VALUES ('a-1', 'tenant-a'), ('b-1', 'tenant-b')", f.parent))
	exec(fmt.Sprintf("INSERT INTO %s VALUES ('ca-1', 'a-1'), ('cb-1', 'b-1')", f.child))
	return f
}

func TestStrictPolicyIsolatesTenants(t *testing.T) {
	conn := openRLSConn(t)
	f := setupRLSFixture(t, conn)
	ctx := context.Background()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	mustExec := func(query string) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	mustExec(fmt.Sprintf("SET LOCAL ROLE %s", f.role))
	mustExec("SET LOCAL app.tenant_id = 'tenant-a'")

	var parents, children int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", f.parent)).Scan(&parents); err != nil {
		t.Fatalf("count parents: %v", err)
	}
	if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", f.child)).Scan(&children); err != nil {
		t.Fatalf("count children: %v", err)
	}
	if parents != 1 {
		t.Fatalf("expected exactly tenant-a's parent row, got %d", parents)
	}
	if children != 1 {
		t.Fatalf("expected exactly tenant-a's child row, got %d", children)
	}

	if _, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s VALUES ('a-2', 'tenant-b')", f.parent)); err == nil {
		t.Fatalf("cross-tenant parent insert was accepted under a strict policy")
	} else if !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("expected a row-level security rejection, got %v", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s VALUES ('cb-2', 'b-1')", f.child)); err == nil {
		t.Fatalf("child insert referencing another tenant's parent was accepted")
	}
}

// TestShadowTriggerSkipsCascadeDelete covers the child-table shadow audit. An
// ON DELETE CASCADE removes the child after the parent row is gone, so the
// parent lookup cannot attribute the delete; that is not a tenant violation.
// A child written against another tenant's parent must still be recorded.
func TestShadowTriggerSkipsCascadeDelete(t *testing.T) {
	conn := openRLSConn(t)
	ctx := context.Background()

	var installed bool
	if err := conn.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'rls_shadow_violation_child')").Scan(&installed); err != nil {
		t.Fatalf("probe shadow function: %v", err)
	}
	if !installed {
		t.Skip("rls_shadow_violation_child is not installed; run migrations first")
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	parent := "shadow_parent_" + suffix
	child := "shadow_child_" + suffix

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	exec := func(query string) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	exec(fmt.Sprintf("CREATE TABLE %s (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL)", parent))
	exec(fmt.Sprintf("CREATE TABLE %s (id TEXT PRIMARY KEY, parent_id TEXT NOT NULL REFERENCES %s(id) ON DELETE CASCADE)", child, parent))
	exec(fmt.Sprintf(
		"CREATE TRIGGER rls_shadow_write AFTER INSERT OR UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION rls_shadow_violation_child('%s', 'parent_id')",
		child, parent))
	exec("SET LOCAL app.tenant_id = 'tenant-a'")
	exec(fmt.Sprintf("INSERT INTO %s VALUES ('a-1', 'tenant-a'), ('b-1', 'tenant-b')", parent))
	exec(fmt.Sprintf("INSERT INTO %s VALUES ('ca-1', 'a-1')", child))

	exec(fmt.Sprintf("DELETE FROM %s WHERE id = 'a-1'", parent))
	var cascadeFindings int
	if err := tx.QueryRowContext(ctx,
		"SELECT count(*) FROM rls_shadow_violations WHERE table_name = $1", child).Scan(&cascadeFindings); err != nil {
		t.Fatalf("count cascade findings: %v", err)
	}
	if cascadeFindings != 0 {
		t.Fatalf("cascade delete recorded %d shadow violations, want 0", cascadeFindings)
	}

	exec(fmt.Sprintf("INSERT INTO %s VALUES ('cb-1', 'b-1')", child))
	var crossFindings int
	if err := tx.QueryRowContext(ctx,
		"SELECT count(*) FROM rls_shadow_violations WHERE table_name = $1 AND row_tenant_id = 'tenant-b'", child).Scan(&crossFindings); err != nil {
		t.Fatalf("count cross-tenant findings: %v", err)
	}
	if crossFindings != 1 {
		t.Fatalf("cross-tenant child write recorded %d findings, want 1", crossFindings)
	}
}

func TestStrictPolicyFailsClosedWithoutTenantContext(t *testing.T) {
	conn := openRLSConn(t)
	f := setupRLSFixture(t, conn)
	ctx := context.Background()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL ROLE %s", f.role)); err != nil {
		t.Fatalf("assume runtime role: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "RESET app.tenant_id"); err != nil {
		t.Fatalf("clear app.tenant_id: %v", err)
	}

	var parents, children int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", f.parent)).Scan(&parents); err != nil {
		t.Fatalf("count parents without GUC: %v", err)
	}
	if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", f.child)).Scan(&children); err != nil {
		t.Fatalf("count children without GUC: %v", err)
	}
	if parents != 0 || children != 0 {
		t.Fatalf("queries without app.tenant_id returned rows: parents=%d children=%d", parents, children)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s VALUES ('a-3', 'tenant-a')", f.parent)); err == nil {
		t.Fatalf("insert without app.tenant_id was accepted")
	} else if !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("expected a row-level security rejection, got %v", err)
	}
}
