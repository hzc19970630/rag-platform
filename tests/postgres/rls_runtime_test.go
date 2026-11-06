// Postgres-gated direct-SQL RLS test. It exercises the strict policies on the
// real audit_logs table as the rag_app runtime role, not a scratch table, and
// requires the migration owner plus `make m13-rls-setup`.
package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRuntimeRoleCannotBypassStrictRLS(t *testing.T) {
	conn := openRLSConn(t)
	ctx := context.Background()

	var strictPolicy bool
	if err := conn.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = 'public' AND tablename = 'audit_logs' AND policyname = 'tenant_isolation')",
	).Scan(&strictPolicy); err != nil {
		t.Fatalf("probe strict policy: %v", err)
	}
	if !strictPolicy {
		t.Skip("audit_logs has no tenant_isolation policy; run migrations to apply 0024")
	}

	var canSeed bool
	if err := conn.QueryRowContext(ctx,
		"SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user",
	).Scan(&canSeed); err != nil {
		t.Fatalf("probe owner role: %v", err)
	}
	if !canSeed {
		t.Skip("TEST_POSTGRES_URL must use the migration owner (superuser or BYPASSRLS) to seed two tenants")
	}

	var hasRole, canSelect, canInsert bool
	if err := conn.QueryRowContext(ctx, "SELECT to_regrole('rag_app') IS NOT NULL").Scan(&hasRole); err != nil {
		t.Fatalf("probe rag_app role: %v", err)
	}
	if !hasRole {
		t.Skip("rag_app does not exist; run make m13-rls-setup")
	}
	if err := conn.QueryRowContext(ctx,
		"SELECT has_table_privilege('rag_app', 'audit_logs', 'SELECT'), has_table_privilege('rag_app', 'audit_logs', 'INSERT')",
	).Scan(&canSelect, &canInsert); err != nil {
		t.Fatalf("probe rag_app grants: %v", err)
	}
	if !canSelect || !canInsert {
		t.Skip("rag_app lacks audit_logs privileges; run make m13-rls-setup")
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	suffix := time.Now().UnixNano()
	userID := fmt.Sprintf("rls-probe-user-%d", suffix)
	tenantA := fmt.Sprintf("rls-probe-a-%d", suffix)
	tenantB := fmt.Sprintf("rls-probe-b-%d", suffix)

	exec("INSERT INTO users (id, email, display_name) VALUES ($1, $2, $3)", userID, userID+"@example.test", "RLS Probe")
	exec("INSERT INTO tenants (id, name) VALUES ($1, $2), ($3, $4)", tenantA, "RLS Probe A", tenantB, "RLS Probe B")
	exec(`INSERT INTO audit_logs (tenant_id, actor_id, action, resource_type, resource_id, allowed)
		VALUES ($1, $2, 'rls_probe', 'chunk', 'probe-a', true),
		       ($3, $2, 'rls_probe', 'chunk', 'probe-b', true)`, tenantA, userID, tenantB)

	exec("SET LOCAL ROLE rag_app")
	exec("SELECT set_config('app.tenant_id', $1, true)", tenantA)

	var visible, own int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id = $1", tenantB).Scan(&visible); err != nil {
		t.Fatalf("cross-tenant select: %v", err)
	}
	if visible != 0 {
		t.Fatalf("rag_app saw %d rows from another tenant under a strict policy", visible)
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id = $1", tenantA).Scan(&own); err != nil {
		t.Fatalf("own-tenant select: %v", err)
	}
	if own != 1 {
		t.Fatalf("rag_app saw %d own-tenant rows, want 1", own)
	}

	exec("SAVEPOINT before_cross_insert")
	_, err = tx.ExecContext(ctx,
		`INSERT INTO audit_logs (tenant_id, actor_id, action, resource_type, resource_id, allowed)
		 VALUES ($1, $2, 'rls_probe', 'chunk', 'probe-cross', true)`, tenantB, userID)
	if err == nil {
		t.Fatalf("rag_app inserted a row for another tenant under a strict policy")
	}
	if !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("expected a row-level security rejection, got %v", err)
	}
	exec("ROLLBACK TO SAVEPOINT before_cross_insert")

	exec("RESET app.tenant_id")
	var withoutContext int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM audit_logs WHERE tenant_id = $1", tenantA).Scan(&withoutContext); err != nil {
		t.Fatalf("select without tenant context: %v", err)
	}
	if withoutContext != 0 {
		t.Fatalf("rag_app saw %d rows without app.tenant_id", withoutContext)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_logs (tenant_id, actor_id, action, resource_type, resource_id, allowed)
		 VALUES ($1, $2, 'rls_probe', 'chunk', 'probe-nocontext', true)`, tenantA, userID); err == nil {
		t.Fatalf("rag_app inserted a row without app.tenant_id")
	} else if !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("expected a fail-closed row-level security rejection, got %v", err)
	}
}
