// Package tenantdb centralizes tenant scoping for GORM queries. Every query
// against a tenant-owned table must go through Scoped; Unscoped is the only
// explicit bypass and requires a reason. The static tenancy gate in
// internal/archtest covers raw SQL in the same way.
package tenantdb

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"
)

// ErrTenantRequired is returned when a query would run without a tenant.
var ErrTenantRequired = errors.New("tenant id is required")

// RLSMode mirrors TENANT_RLS_MODE. Off leaves the PostgreSQL session untouched,
// shadow exposes app.tenant_id so the write-audit triggers can compare it, and
// enforce expects strict policies on top of the same context.
type RLSMode string

const (
	RLSModeOff     RLSMode = "off"
	RLSModeShadow  RLSMode = "shadow"
	RLSModeEnforce RLSMode = "enforce"
)

var rlsMode atomic.Value

func init() {
	rlsMode.Store(RLSModeShadow)
}

// ConfigureRLSMode applies TENANT_RLS_MODE. An empty value keeps the shadow
// default so a zero-value config cannot turn RLS context off silently.
func ConfigureRLSMode(raw string) error {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(RLSModeShadow):
		rlsMode.Store(RLSModeShadow)
	case string(RLSModeOff):
		rlsMode.Store(RLSModeOff)
	case string(RLSModeEnforce):
		rlsMode.Store(RLSModeEnforce)
	default:
		return fmt.Errorf("unsupported tenant rls mode %q", raw)
	}
	return nil
}

// CurrentRLSMode reports the process-wide mode.
func CurrentRLSMode() RLSMode {
	mode, _ := rlsMode.Load().(RLSMode)
	if mode == "" {
		return RLSModeShadow
	}
	return mode
}

func isPostgres(db *gorm.DB) bool {
	return db.Dialector != nil && db.Dialector.Name() == "postgres"
}

// Require rejects an empty tenant. Empty no longer means "all tenants"; a
// cross-tenant pass has to use Unscoped with an explicit reason.
func Require(tenantID string) error {
	if strings.TrimSpace(tenantID) == "" {
		return ErrTenantRequired
	}
	return nil
}

// Scoped adds tenant_id = ? to the query. It is dialect-independent and only
// valid for tables that own a tenant_id column; the four child tables
// (stage_runs, projection_stage_runs, knowledge_base_members, task_inbox) must
// be scoped through their parent instead.
func Scoped(db *gorm.DB, tenantID string) (*gorm.DB, error) {
	if err := Require(tenantID); err != nil {
		return nil, err
	}
	if requestTx := requestTxFor(db); requestTx != nil {
		db = requestTx.WithContext(db.Statement.Context)
	}
	if CurrentRLSMode() != RLSModeOff && isPostgres(db) {
		// Transaction-local: outside a transaction PostgreSQL scopes this to
		// the single implicit transaction, so nothing leaks across pooled
		// connections. Callers that need it for a whole request use InTx.
		if err := setTenantContext(db, tenantID); err != nil {
			return nil, err
		}
	}
	return db.Where("tenant_id = ?", tenantID), nil
}

// MustScoped is Scoped for call sites whose principal was already validated at
// the request boundary. A missing tenant fails closed (WHERE 1 = 0) and is
// logged instead of silently returning every tenant's rows.
func MustScoped(db *gorm.DB, tenantID string) *gorm.DB {
	scoped, err := Scoped(db, tenantID)
	if err != nil {
		slog.Error("tenant_scope_missing", "error", err)
		return db.Where("1 = 0")
	}
	return scoped
}

var bypassLogged sync.Map

// Unscoped is the single explicit cross-tenant bypass. The reason is part of
// the audit trail; an empty reason fails closed like a missing tenant.
func Unscoped(db *gorm.DB, reason string) *gorm.DB {
	if strings.TrimSpace(reason) == "" {
		slog.Error("tenant_scope_bypass_without_reason")
		return db.Where("1 = 0")
	}
	if _, logged := bypassLogged.LoadOrStore(reason, struct{}{}); !logged {
		slog.Info("tenant_scope_bypassed", "reason", reason)
	}
	return db
}
