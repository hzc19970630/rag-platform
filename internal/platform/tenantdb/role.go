package tenantdb

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// RoleAttributes are the PostgreSQL role flags that decide whether row-level
// security applies. A superuser or BYPASSRLS role silently ignores every policy,
// so enforce mode verifies the live connection instead of trusting the URL.
type RoleAttributes struct {
	CurrentUser string
	SuperUser   bool
	BypassRLS   bool
}

// RoleAttributesOf reads the flags of the connected login role.
func RoleAttributesOf(ctx context.Context, db *gorm.DB) (RoleAttributes, error) {
	var attrs RoleAttributes
	row := db.WithContext(ctx).Raw(
		"SELECT current_user, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user",
	).Row()
	if err := row.Scan(&attrs.CurrentUser, &attrs.SuperUser, &attrs.BypassRLS); err != nil {
		return attrs, fmt.Errorf("inspect database role: %w", err)
	}
	return attrs, nil
}

// VerifyRuntimeRole rejects a runtime connection that would bypass RLS.
func VerifyRuntimeRole(ctx context.Context, db *gorm.DB) error {
	attrs, err := RoleAttributesOf(ctx, db)
	if err != nil {
		return err
	}
	if attrs.SuperUser || attrs.BypassRLS {
		return fmt.Errorf("runtime database role %q bypasses row-level security (superuser=%t bypassrls=%t)",
			attrs.CurrentUser, attrs.SuperUser, attrs.BypassRLS)
	}
	return nil
}

// VerifyMaintenanceRole requires the maintenance connection to bypass RLS: its
// whole purpose is cross-tenant sweeps that strict policies would hide.
func VerifyMaintenanceRole(ctx context.Context, db *gorm.DB) error {
	attrs, err := RoleAttributesOf(ctx, db)
	if err != nil {
		return err
	}
	if !attrs.SuperUser && !attrs.BypassRLS {
		return fmt.Errorf("maintenance database role %q must have BYPASSRLS", attrs.CurrentUser)
	}
	return nil
}
