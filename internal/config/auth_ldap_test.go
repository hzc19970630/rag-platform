package config

import "testing"

func ldapAuthConfig() AuthConfig {
	return AuthConfig{
		SessionTTLSeconds:   3600,
		JWTIssuer:           "rag-platform",
		JWTAudience:         "rag-platform-api",
		SigningKeys:         []string{"test:0123456789abcdef0123456789abcdef"},
		SigningKeysExplicit: true,
		ActiveKeyID:         "test",
		CookieSecure:        true,
		APIKeyPepper:        "pepper",
		LDAP: LDAPConfig{
			Enabled:        true,
			URL:            "ldap://ldap.example.com:389",
			BindDN:         "cn=reader,dc=example,dc=com",
			BindPassword:   "secret",
			BaseDN:         "dc=example,dc=com",
			UserFilter:     "(uid=%s)",
			TenantID:       "tenant-a",
			SyncUserFilter: "(objectClass=person)",
			GroupRoleMap:   "cn=rag-admins,ou=groups,dc=example,dc=com=admin;rag-viewers=viewer",
		},
	}
}

func TestLDAPConfigValidation(t *testing.T) {
	cfg := ldapAuthConfig()
	if err := cfg.Validate("development"); err != nil {
		t.Fatalf("valid LDAP config rejected: %v", err)
	}

	missingTenant := ldapAuthConfig()
	missingTenant.LDAP.TenantID = ""
	if err := missingTenant.Validate("development"); err == nil {
		t.Fatal("LDAP without a tenant must fail startup")
	}

	badFilter := ldapAuthConfig()
	badFilter.LDAP.UserFilter = "(uid=admin)"
	if err := badFilter.Validate("development"); err == nil {
		t.Fatal("LDAP user filter without the username placeholder must fail startup")
	}

	badRole := ldapAuthConfig()
	badRole.LDAP.GroupRoleMap = "cn=rag-admins=superuser"
	if err := badRole.Validate("development"); err == nil {
		t.Fatal("unknown mapped role must fail startup")
	}
}

func TestProductionLDAPRequiresTLS(t *testing.T) {
	cfg := ldapAuthConfig()
	if err := cfg.Validate("production"); err == nil {
		t.Fatal("production plain LDAP must fail without StartTLS or an explicit acknowledgement")
	}
	cfg.LDAP.StartTLS = true
	if err := cfg.Validate("production"); err != nil {
		t.Fatalf("production StartTLS LDAP rejected: %v", err)
	}
	plain := ldapAuthConfig()
	plain.LDAP.AllowInsecure = true
	if err := plain.Validate("production"); err != nil {
		t.Fatalf("explicit LDAP_ALLOW_INSECURE acknowledgement rejected: %v", err)
	}
}
