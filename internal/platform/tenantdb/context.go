package tenantdb

import "context"

type tenantKey struct{}

// WithTenant carries the tenant through async work (queued tasks, maintenance
// loops) that no longer has the HTTP request context.
func WithTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenantID)
}

// TenantFrom returns the tenant carried by the context.
func TenantFrom(ctx context.Context) (string, bool) {
	tenantID, ok := ctx.Value(tenantKey{}).(string)
	return tenantID, ok && tenantID != ""
}
