package tenantdb

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"gorm.io/gorm"
)

// txContextKey carries the request transaction. Every query built from a
// non-transaction handle while this value is present is redirected onto it, so
// existing service code keeps its shape while sharing one tenant-scoped
// connection and transaction.
type txContextKey struct{}

// requestTxState holds the request transaction plus the poisoned flag. A
// service-level transaction that fails inside the request must roll the whole
// request back: the request transaction has no savepoints of its own.
type requestTxState struct {
	db       *gorm.DB
	rollback atomic.Bool
}

// WithTx attaches a request transaction to the context.
func WithTx(ctx context.Context, tx *gorm.DB) context.Context {
	if tx == nil {
		return ctx
	}
	return context.WithValue(ctx, txContextKey{}, &requestTxState{db: tx})
}

func txStateFrom(ctx context.Context) *requestTxState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(txContextKey{}).(*requestTxState)
	return state
}

// TxFrom returns the request transaction, if any.
func TxFrom(ctx context.Context) *gorm.DB {
	if state := txStateFrom(ctx); state != nil {
		return state.db
	}
	return nil
}

// RequestTxActive reports whether a request transaction is attached. It pins
// one driver connection for the whole request, and that connection cannot run
// two statements at once, so callers that would fan out queries must run them
// sequentially while it is active.
func RequestTxActive(ctx context.Context) bool {
	return txStateFrom(ctx) != nil
}

// MarkRollback poisons the request transaction. It is used when a nested
// service transaction fails: the statement-level callback cannot roll one group
// back on its own, so the request middleware rolls everything back instead.
func MarkRollback(ctx context.Context) {
	if state := txStateFrom(ctx); state != nil {
		state.rollback.Store(true)
	}
}

// FromContext returns the request transaction when one is attached; otherwise
// it returns db bound to ctx. Raw-SQL call sites use it the same way the GORM
// callback redirect covers chained queries.
func FromContext(ctx context.Context, db *gorm.DB) *gorm.DB {
	if tx := TxFrom(ctx); tx != nil {
		return tx.WithContext(ctx)
	}
	return db.WithContext(ctx)
}

func inTransaction(db *gorm.DB) bool {
	if db == nil || db.Statement == nil {
		return false
	}
	_, ok := db.Statement.ConnPool.(gorm.TxCommitter)
	return ok
}

// requestTxFor reports the transaction a statement should join. A handle that
// already runs inside a transaction keeps it: service-level transactions must
// remain atomic even though the request context carries a wider one.
func requestTxFor(db *gorm.DB) *gorm.DB {
	if inTransaction(db) || db.Statement == nil {
		return nil
	}
	return TxFrom(db.Statement.Context)
}

// InstallRequestTxCallbacks routes statements issued on pooled handles through
// the request transaction from their context. The callbacks are registered
// before the default-transaction hooks so GORM does not open a second
// transaction per statement.
func InstallRequestTxCallbacks(db *gorm.DB) {
	if db == nil {
		return
	}
	redirect := func(tx *gorm.DB) {
		requestTx := requestTxFor(tx)
		if requestTx == nil || requestTx.Statement == nil {
			return
		}
		tx.Statement.ConnPool = requestTx.Statement.ConnPool
	}
	db.Callback().Create().Before("gorm:begin_transaction").Register("tenant:request_tx", redirect)
	db.Callback().Update().Before("gorm:begin_transaction").Register("tenant:request_tx", redirect)
	db.Callback().Delete().Before("gorm:begin_transaction").Register("tenant:request_tx", redirect)
	db.Callback().Query().Before("gorm:query").Register("tenant:request_tx", redirect)
	db.Callback().Row().Before("gorm:row").Register("tenant:request_tx", redirect)
	db.Callback().Raw().Before("gorm:raw").Register("tenant:request_tx", redirect)
}

// BeginRequestTx opens the tenant transaction that spans one API request. It
// is a no-op outside enforce mode and on non-PostgreSQL databases, which keeps
// shadow traffic and the SQLite test suite unchanged. finish rolls the
// transaction back on error and commits otherwise.
func BeginRequestTx(ctx context.Context, db *gorm.DB, tenantID string) (context.Context, func(error), error) {
	noop := func(error) {}
	if CurrentRLSMode() != RLSModeEnforce || !isPostgres(db) {
		return ctx, noop, nil
	}
	if err := Require(tenantID); err != nil {
		return ctx, nil, err
	}
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return ctx, nil, fmt.Errorf("begin tenant request transaction: %w", tx.Error)
	}
	if err := setTenantContext(tx, tenantID); err != nil {
		_ = tx.Rollback().Error
		return ctx, nil, err
	}
	ctx = WithTx(ctx, tx)
	state := txStateFrom(ctx)
	finish := func(cause error) {
		if cause != nil || state.rollback.Load() {
			if err := tx.Rollback().Error; err != nil {
				slog.Error("tenant_request_rollback_failed", "error", err)
			}
			return
		}
		if err := tx.Commit().Error; err != nil {
			// The response is already written at this point; losing the commit is
			// loud on purpose rather than silently swallowing tenant writes.
			slog.Error("tenant_request_commit_failed", "tenant_id", tenantID, "error", err)
		}
	}
	return ctx, finish, nil
}

// InTx is the service-level transaction entry point. Inside a request it joins
// the request transaction: strict policies only see app.tenant_id on that
// connection, and a second connection would fail closed. Outside enforce mode
// it keeps the original transaction semantics so SQLite tests and shadow
// deployments are unchanged.
func InTx(ctx context.Context, db *gorm.DB, tenantID string, fn func(tx *gorm.DB) error) error {
	if state := txStateFrom(ctx); state != nil {
		err := fn(state.db.WithContext(ctx))
		if err != nil {
			state.rollback.Store(true)
		}
		return err
	}
	if CurrentRLSMode() != RLSModeEnforce || !isPostgres(db) {
		return db.WithContext(ctx).Transaction(fn)
	}
	if err := Require(tenantID); err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := setTenantContext(tx, tenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func setTenantContext(tx *gorm.DB, tenantID string) error {
	if err := tx.Exec("SELECT set_config('app.tenant_id', ?, true)", tenantID).Error; err != nil {
		return fmt.Errorf("set tenant rls context: %w", err)
	}
	return nil
}
