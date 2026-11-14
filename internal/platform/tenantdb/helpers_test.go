package tenantdb

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestRoleVerificationReportsDatabaseErrors(t *testing.T) {
	db := openInjectDB(t)
	ctx := context.Background()
	if _, err := RoleAttributesOf(ctx, db); err == nil {
		t.Fatal("RoleAttributesOf must fail without pg_roles")
	}
	if err := VerifyRuntimeRole(ctx, db); err == nil {
		t.Fatal("VerifyRuntimeRole must surface the inspection error")
	}
	if err := VerifyMaintenanceRole(ctx, db); err == nil {
		t.Fatal("VerifyMaintenanceRole must surface the inspection error")
	}
}

func TestTenantTablesReturnsSortedCopy(t *testing.T) {
	tables := TenantTables()
	if len(tables) == 0 {
		t.Fatal("tenant table catalog is empty")
	}
	for index := 1; index < len(tables); index++ {
		if tables[index-1] > tables[index] {
			t.Fatalf("tenant tables are not sorted: %q > %q", tables[index-1], tables[index])
		}
	}
	tables[0] = "mutated"
	if TenantTables()[0] == "mutated" {
		t.Fatal("TenantTables must return a copy")
	}
}

func TestConfigureRLSMode(t *testing.T) {
	t.Cleanup(func() { _ = ConfigureRLSMode(string(RLSModeShadow)) })
	if err := ConfigureRLSMode("unsupported"); err == nil {
		t.Fatal("unknown mode must be rejected")
	}
	for _, mode := range []RLSMode{RLSModeOff, RLSModeEnforce, RLSModeShadow} {
		if err := ConfigureRLSMode(string(mode)); err != nil {
			t.Fatalf("configure %q: %v", mode, err)
		}
		if CurrentRLSMode() != mode {
			t.Fatalf("CurrentRLSMode = %q, want %q", CurrentRLSMode(), mode)
		}
	}
	if err := ConfigureRLSMode(""); err != nil || CurrentRLSMode() != RLSModeShadow {
		t.Fatalf("empty mode = %q, %v", CurrentRLSMode(), err)
	}
}

func TestTenantExpressionDetection(t *testing.T) {
	column := clause.Column{Name: "tenant_id", Table: "chunks"}
	expressions := []clause.Expression{
		clause.Eq{Column: column},
		clause.Neq{Column: column},
		clause.Gt{Column: column},
		clause.Gte{Column: column},
		clause.Lt{Column: column},
		clause.Lte{Column: column},
		clause.Like{Column: column},
		clause.IN{Column: column},
		clause.AndConditions{Exprs: []clause.Expression{clause.Eq{Column: column}}},
		clause.OrConditions{Exprs: []clause.Expression{clause.Eq{Column: column}}},
		clause.NotConditions{Exprs: []clause.Expression{clause.Eq{Column: column}}},
		clause.Expr{SQL: "x.tenant_id = ?"},
		clause.NamedExpr{SQL: "tenant_id = @tenant"},
	}
	for _, expression := range expressions {
		if !expressionHasTenant(expression) {
			t.Fatalf("%T must be detected as tenant-scoped", expression)
		}
	}
	if expressionHasTenant(clause.Eq{Column: clause.Column{Name: "id"}}) {
		t.Fatal("non-tenant columns must not match")
	}
	if expressionHasTenant(clause.Eq{}) {
		t.Fatal("empty expression must not match")
	}
	if expressionsHaveTenant(nil) || expressionsHaveTenant([]clause.Expression{}) {
		t.Fatal("empty expression lists must not match")
	}
	if !expressionsHaveTenant([]clause.Expression{clause.Eq{Column: clause.Column{Name: "id"}}, clause.Eq{Column: column}}) {
		t.Fatal("mixed expression lists must match")
	}
}

func TestIsTenantColumnVariants(t *testing.T) {
	column := clause.Column{Name: "tenant_id"}
	pointer := column
	cases := []struct {
		value any
		want  bool
	}{
		{column, true},
		{&pointer, true},
		{"tenant_id = ?", true},
		{"chunks.tenant_id", true},
		{clause.Expr{SQL: "tenant_id = ?"}, true},
		{clause.Column{Name: "other"}, false},
		{(*clause.Column)(nil), false},
		{"other", false},
		{42, false},
	}
	for _, testCase := range cases {
		if got := isTenantColumn(testCase.value); got != testCase.want {
			t.Fatalf("isTenantColumn(%v) = %v, want %v", testCase.value, got, testCase.want)
		}
	}
	if !referencesTenant("TENANT_ID = ?") {
		t.Fatal("referencesTenant must be case-insensitive")
	}
	if referencesTenant("id = ?") {
		t.Fatal("referencesTenant must ignore other columns")
	}
}

func TestRequestTransactionLifecycle(t *testing.T) {
	db := openInjectDB(t)
	InstallRequestTxCallbacks(nil)

	ctx := context.Background()
	if RequestTxActive(ctx) || TxFrom(ctx) != nil {
		t.Fatal("context without a transaction must be inactive")
	}
	if got := FromContext(ctx, db); got == nil {
		t.Fatal("FromContext must return the bound handle")
	}
	if WithTx(ctx, nil) != ctx {
		t.Fatal("WithTx(nil) must keep the context")
	}

	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin: %v", tx.Error)
	}
	active := WithTx(ctx, tx)
	if !RequestTxActive(active) || TxFrom(active) != tx {
		t.Fatal("attached request transaction must be visible")
	}
	if redirected := FromContext(active, db); redirected.Statement == nil || redirected.Statement.Context != active {
		t.Fatal("FromContext must prefer the request transaction")
	}
	MarkRollback(active)
	state := txStateFrom(active)
	if state == nil || !state.rollback.Load() {
		t.Fatal("MarkRollback must poison the request transaction")
	}
	if err := CommitRequestTx(active); err != nil {
		t.Fatalf("CommitRequestTx: %v", err)
	}
	if RequestTxActive(active) {
		t.Fatal("committed transaction must be inactive")
	}
	if err := CommitRequestTx(active); err != nil {
		t.Fatalf("second CommitRequestTx = %v", err)
	}

	if ctx, finish, err := BeginRequestTx(context.Background(), db, "tenant-a"); err != nil || finish == nil {
		t.Fatalf("BeginRequestTx shadow = %v", err)
	} else {
		finish(nil)
		_ = ctx
	}
	t.Cleanup(func() { _ = ConfigureRLSMode(string(RLSModeShadow)) })
	if err := ConfigureRLSMode(string(RLSModeEnforce)); err != nil {
		t.Fatalf("enforce mode: %v", err)
	}
	if _, finish, err := BeginRequestTx(context.Background(), db, "tenant-a"); err != nil || finish == nil {
		t.Fatalf("BeginRequestTx sqlite enforce = %v", err)
	}
}

func TestInTxJoinsRequestTransactionFailure(t *testing.T) {
	db := openInjectDB(t)
	ctx := context.Background()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatalf("begin: %v", tx.Error)
	}
	active := WithTx(ctx, tx)
	cause := errors.New("nested failure")
	err := InTx(active, db, "tenant-a", func(*gorm.DB) error { return cause })
	if !errors.Is(err, cause) {
		t.Fatalf("InTx error = %v", err)
	}
	state := txStateFrom(active)
	if state == nil || !state.rollback.Load() {
		t.Fatal("InTx failure must poison the request transaction")
	}
	_ = tx.Rollback()
}
