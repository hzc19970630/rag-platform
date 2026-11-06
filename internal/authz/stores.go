package authz

import "gorm.io/gorm"

// NewPostgresStores wires the Postgres adapters behind every authz port.
func NewPostgresStores(db *gorm.DB) Stores {
	return Stores{
		Policy:     NewPostgresPolicyStore(db),
		Scope:      NewPostgresScopeStore(db),
		Audit:      NewPostgresAuditStore(db),
		Membership: NewPostgresMembershipStore(db),
	}
}
