package models

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// GORM's default naming strategy splits consecutive initials, so OIDCIssuer
// would map to o_id_c_issuer. The columns are part of migration 0007; keep the
// mapping pinned so OIDC account linking cannot regress at runtime.
func TestUserOIDCColumnNames(t *testing.T) {
	parsed, err := schema.Parse(&User{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse User schema: %v", err)
	}
	for field, column := range map[string]string{
		"OIDCIssuer":  "oidc_issuer",
		"OIDCSubject": "oidc_subject",
	} {
		parsedField := parsed.LookUpField(field)
		if parsedField == nil {
			t.Fatalf("field %s not found", field)
		}
		if parsedField.DBName != column {
			t.Fatalf("field %s maps to %s, want %s", field, parsedField.DBName, column)
		}
	}
}
