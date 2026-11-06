package tenantdb

import (
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync/atomic"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// bypassSetting marks a statement built through Unscoped. The tenant injection
// callback skips it; the reason is already logged by Unscoped.
const bypassSetting = "tenantdb:bypass"

// ErrTenantScopeMismatch is returned when a row already carries another
// tenant's id. The callback fails closed instead of silently rewriting it.
var ErrTenantScopeMismatch = errors.New("row tenant does not match the request tenant")

var scopeInjections atomic.Int64

// ScopeInjectionCount reports how many statements the callback had to scope.
// It is used by tests and can back an operator metric later.
func ScopeInjectionCount() int64 { return scopeInjections.Load() }

// ResetScopeInjectionCount resets the counter. Tests use it to isolate runs.
func ResetScopeInjectionCount() { scopeInjections.Store(0) }

// InstallTenantScopeCallbacks adds tenant_id to GORM statements that run with
// a tenant context. It is defense in depth for the GORM half of the query
// surface: callers should still use Scoped/MustScoped, and the static gate
// covers direct chains. Maintenance code that must cross tenants opts out with
// Unscoped.
func InstallTenantScopeCallbacks(db *gorm.DB) {
	if db == nil {
		return
	}
	db.Callback().Query().Before("gorm:query").Register("tenant:scope", injectTenantQuery)
	db.Callback().Update().Before("gorm:update").Register("tenant:scope", injectTenantWrite)
	db.Callback().Delete().Before("gorm:delete").Register("tenant:scope", injectTenantWrite)
	db.Callback().Create().Before("gorm:create").Register("tenant:scope", injectTenantCreate)
}

func injectTenantQuery(tx *gorm.DB) {
	tenant, table, ok := scopeTarget(tx)
	if !ok || hasTenantPredicate(tx.Statement) {
		return
	}
	addTenantPredicate(tx, table, tenant)
}

func injectTenantWrite(tx *gorm.DB) {
	injectTenantQuery(tx)
}

func injectTenantCreate(tx *gorm.DB) {
	tenant, _, ok := scopeTarget(tx)
	if !ok || tx.Statement.Schema == nil {
		return
	}
	field := tx.Statement.Schema.LookUpField("TenantID")
	if field == nil {
		return
	}
	value := tx.Statement.ReflectValue
	if !value.IsValid() {
		tx.Statement.SetColumn("TenantID", tenant)
		scopeInjections.Add(1)
		return
	}
	for value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	var err error
	switch value.Kind() {
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err = setTenantValue(tx, field, value.Index(index), tenant); err != nil {
				break
			}
		}
	default:
		err = setTenantValue(tx, field, value, tenant)
	}
	if err != nil {
		_ = tx.AddError(err)
		return
	}
	scopeInjections.Add(1)
	slog.Debug("tenant_scope_create", "table", tx.Statement.Schema.Table, "tenant", tenant)
}

func setTenantValue(tx *gorm.DB, field *schema.Field, value reflect.Value, tenant string) error {
	for value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	current, _ := field.ValueOf(tx.Statement.Context, value)
	switch typed := current.(type) {
	case string:
		if typed != "" && typed != tenant {
			return fmt.Errorf("%w: %s has %q", ErrTenantScopeMismatch, field.DBName, typed)
		}
	case *string:
		if typed != nil && *typed != "" && *typed != tenant {
			return fmt.Errorf("%w: %s has %q", ErrTenantScopeMismatch, field.DBName, *typed)
		}
	}
	return field.Set(tx.Statement.Context, value, tenant)
}

// scopeTarget reports whether this statement should be tenant-scoped. It
// requires a tenant in context, a tenant-owned model/table and no bypass.
func scopeTarget(tx *gorm.DB) (tenant, table string, ok bool) {
	if tx == nil || tx.Statement == nil {
		return "", "", false
	}
	if CurrentRLSMode() == RLSModeOff {
		return "", "", false
	}
	tenant, ok = TenantFrom(tx.Statement.Context)
	if !ok {
		return "", "", false
	}
	if bypassed(tx.Statement) {
		return "", "", false
	}
	table, owned := tenantOwnedTable(tx.Statement)
	if !owned {
		return "", "", false
	}
	return tenant, table, true
}

func bypassed(statement *gorm.Statement) bool {
	if statement == nil {
		return false
	}
	value, ok := statement.Settings.Load(bypassSetting)
	if !ok {
		return false
	}
	flag, _ := value.(bool)
	return flag
}

// tenantOwnedTable resolves the statement's primary table. Schema inspection
// handles Model/Find/Save chains; the table catalog covers Table(...) chains
// whose destination struct does not embed TenantID.
func tenantOwnedTable(statement *gorm.Statement) (string, bool) {
	table := statement.Table
	if statement.Schema != nil {
		if table == "" {
			table = statement.Schema.Table
		}
		if statement.Schema.LookUpField("TenantID") != nil {
			return tableName(table, statement.Schema.Table), true
		}
		if statement.Table != "" {
			if _, ok := tenantTableSet[catalogName(statement.Table)]; ok {
				return statement.Table, true
			}
		}
		return tableName(table, statement.Schema.Table), false
	}
	if table == "" {
		return "", false
	}
	name := tableName(table, "")
	if _, ok := tenantTableSet[name]; ok {
		return name, true
	}
	return name, false
}

// tableName keeps a caller-supplied alias (for example "memberships m") and
// falls back to the schema table when no explicit table was set.
func tableName(explicit, fallback string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	return fallback
}

// catalogName strips an optional alias ("chunks c" -> "chunks") so explicit
// Table clauses can be matched against the tenant table catalog.
func catalogName(table string) string {
	fields := strings.Fields(table)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// qualifier returns the identifier to prefix tenant_id with. A table clause may
// carry an alias; qualifying with the alias keeps joins unambiguous.
func qualifier(table string) string {
	fields := strings.Fields(table)
	switch len(fields) {
	case 0:
		return ""
	case 1:
		return fields[0]
	default:
		return fields[len(fields)-1]
	}
}

func addTenantPredicate(tx *gorm.DB, table, tenant string) {
	column := clause.Column{Name: "tenant_id"}
	if alias := qualifier(table); alias != "" {
		column.Table = alias
	}
	tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Eq{Column: column, Value: tenant}}})
	scopeInjections.Add(1)
	slog.Warn("tenant_scope_injected", "table", table, "tenant", tenant, "mode", string(CurrentRLSMode()))
}

func hasTenantPredicate(statement *gorm.Statement) bool {
	if statement == nil {
		return false
	}
	where, ok := statement.Clauses["WHERE"]
	if !ok || where.Expression == nil {
		return false
	}
	conditions, ok := where.Expression.(clause.Where)
	if !ok {
		return false
	}
	for _, expression := range conditions.Exprs {
		if expressionHasTenant(expression) {
			return true
		}
	}
	return false
}

func expressionHasTenant(expression clause.Expression) bool {
	switch typed := expression.(type) {
	case clause.Eq:
		return isTenantColumn(typed.Column)
	case clause.Neq:
		return isTenantColumn(typed.Column)
	case clause.Gt:
		return isTenantColumn(typed.Column)
	case clause.Gte:
		return isTenantColumn(typed.Column)
	case clause.Lt:
		return isTenantColumn(typed.Column)
	case clause.Lte:
		return isTenantColumn(typed.Column)
	case clause.Like:
		return isTenantColumn(typed.Column)
	case clause.IN:
		return isTenantColumn(typed.Column)
	case clause.AndConditions:
		return expressionsHaveTenant(typed.Exprs)
	case clause.OrConditions:
		return expressionsHaveTenant(typed.Exprs)
	case clause.NotConditions:
		return expressionsHaveTenant(typed.Exprs)
	case clause.Expr:
		return referencesTenant(typed.SQL)
	case clause.NamedExpr:
		return referencesTenant(typed.SQL)
	default:
		return false
	}
}

func expressionsHaveTenant(expressions []clause.Expression) bool {
	for _, expression := range expressions {
		if expressionHasTenant(expression) {
			return true
		}
	}
	return false
}

func isTenantColumn(column any) bool {
	switch typed := column.(type) {
	case clause.Column:
		return strings.EqualFold(typed.Name, "tenant_id")
	case *clause.Column:
		return typed != nil && strings.EqualFold(typed.Name, "tenant_id")
	case string:
		return referencesTenant(typed)
	case clause.Expr:
		return referencesTenant(typed.SQL)
	default:
		return false
	}
}

func referencesTenant(sql string) bool {
	return strings.Contains(strings.ToLower(sql), "tenant_id")
}
