package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/go-ldap/ldap/v3"
	"github.com/hzc19970630/rag-platform/internal/apperror"
)

func ldapTestClient(options LDAPOptions) *LDAPClient {
	options.UserIDAttribute = "uid"
	options.EmailAttribute = "mail"
	options.NameAttribute = "cn"
	options.GroupAttribute = "memberOf"
	return NewLDAPClient(options)
}

func TestLDAPClientHelpers(t *testing.T) {
	var nilClient *LDAPClient
	if nilClient.Enabled() {
		t.Fatal("nil client must be disabled")
	}
	if ldapTestClient(LDAPOptions{}).Enabled() {
		t.Fatal("disabled options must report disabled")
	}
	client := ldapTestClient(LDAPOptions{
		Enabled:      true,
		TenantID:     "tenant-1",
		GroupRoleMap: "cn=admins,ou=groups,dc=example,dc=com=admin;analysts=member",
		DefaultRole:  "viewer",
	})
	if !client.Enabled() {
		t.Fatal("enabled options must report enabled")
	}

	cases := []struct {
		name   string
		groups []string
		role   string
		ok     bool
	}{
		{"dn match", []string{"CN=Admins,OU=Groups,DC=Example,DC=Com"}, "admin", true},
		{"cn match", []string{"analysts"}, "member", true},
		{"highest privilege wins", []string{"analysts", "CN=Admins,OU=Groups,DC=Example,DC=Com"}, "admin", true},
		{"default role", []string{"unknown"}, "viewer", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			role, ok := client.RoleForGroups(testCase.groups)
			if role != testCase.role || ok != testCase.ok {
				t.Fatalf("RoleForGroups(%v) = %q, %v", testCase.groups, role, ok)
			}
		})
	}
	client.cfg.DefaultRole = ""
	if role, ok := client.RoleForGroups(nil); ok || role != "" {
		t.Fatalf("unmatched group without default = %q, %v", role, ok)
	}

	if attrs := client.attributes(); len(attrs) != 4 {
		t.Fatalf("attributes without group filter = %v", attrs)
	}
	client.cfg.GroupFilter = "(member=%s)"
	if attrs := client.attributes(); len(attrs) != 3 {
		t.Fatalf("attributes with group filter = %v", attrs)
	}

	entry := &ldap.Entry{DN: "uid=alice,ou=people,dc=example,dc=com", Attributes: []*ldap.EntryAttribute{
		{Name: "uid", Values: []string{"alice"}},
		{Name: "mail", Values: []string{"alice@example.com"}},
		{Name: "cn", Values: []string{"Alice"}},
	}}
	identity := client.identityFromEntry(entry, []string{"admins", "admins", " "})
	if identity.Username != "alice" || identity.Email != "alice@example.com" || identity.Name != "Alice" || identity.TenantID != "tenant-1" {
		t.Fatalf("identity = %+v", identity)
	}
	if len(identity.Groups) != 3 {
		t.Fatalf("identity groups must pass through: %v", identity.Groups)
	}

	fallback := client.identityFromEntry(&ldap.Entry{DN: "cn=x", Attributes: []*ldap.EntryAttribute{{Name: "uid", Values: []string{"fallback"}}}}, nil)
	if fallback.Username != "fallback" {
		t.Fatalf("uid fallback = %q", fallback.Username)
	}

	mapping := parseGroupRoleMap(" ; cn=ops=admin ; broken ; =admin ; cn=empty= ")
	if mapping[normalizeDN("cn=ops")] != "admin" || len(mapping) != 1 {
		t.Fatalf("group role map = %v", mapping)
	}
	if normalizeDN("  CN=Ops,  OU=A ") != "cn=ops, ou=a" {
		t.Fatalf("normalizeDN mismatch")
	}
	if commonName("CN=Ops,OU=Groups") != "Ops" || commonName("uid=x") != "" {
		t.Fatal("commonName mismatch")
	}
	if values := uniqueStrings([]string{"b", " a ", "", "a", "b"}); len(values) != 2 || values[0] != "a" || values[1] != "b" {
		t.Fatalf("uniqueStrings = %v", values)
	}
}

func TestLDAPClientConnectFailures(t *testing.T) {
	client := ldapTestClient(LDAPOptions{URL: "not a url", TimeoutSeconds: 1})
	if _, err := client.Authenticate(context.Background(), "alice", "secret"); err == nil {
		t.Fatal("invalid URL must fail authentication")
	}
	if _, err := client.ListUsers(context.Background()); err == nil {
		t.Fatal("invalid URL must fail user listing")
	}
	if _, err := client.Authenticate(context.Background(), "", "secret"); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("empty username = %v", err)
	}
	if _, err := client.Authenticate(context.Background(), "alice", ""); !errors.Is(err, apperror.ErrUnauthenticated) {
		t.Fatalf("empty password = %v", err)
	}
}

func TestServiceAuthSwitchesAndAudience(t *testing.T) {
	service, err := NewService(nil, AuthOptions{
		SigningKeys: []string{"test:secret"}, ActiveKeyID: "test", SessionTTLSeconds: 3600, APIKeyPepper: "pepper",
		OIDC: OIDCOptions{Enabled: true, Issuer: "https://issuer.example.com"},
		LDAP: LDAPOptions{Enabled: true, URL: "ldap://127.0.0.1:1", TimeoutSeconds: 1},
	})
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	if !service.OIDCEnabled() || !service.LDAPEnabled() || service.OIDCIssuer() != "https://issuer.example.com" {
		t.Fatalf("auth switches = oidc:%v ldap:%v issuer:%q", service.OIDCEnabled(), service.LDAPEnabled(), service.OIDCIssuer())
	}
	disabled, err := NewService(nil, AuthOptions{SigningKeys: []string{"test:secret"}, ActiveKeyID: "test", SessionTTLSeconds: 3600, APIKeyPepper: "pepper"})
	if err != nil {
		t.Fatalf("disabled service: %v", err)
	}
	if disabled.OIDCEnabled() || disabled.LDAPEnabled() {
		t.Fatal("disabled providers must stay off")
	}

	cases := []struct {
		value    any
		expected bool
	}{
		{"aud", true},
		{"other", false},
		{[]any{"a", "aud"}, true},
		{[]any{"a", 1}, false},
		{42, false},
	}
	for _, testCase := range cases {
		if got := audienceContains(testCase.value, "aud"); got != testCase.expected {
			t.Fatalf("audienceContains(%v) = %v", testCase.value, got)
		}
	}

	if _, _, _, _, err := disabled.BeginOIDC(context.Background()); err == nil {
		t.Fatal("BeginOIDC without OIDC must fail")
	}
	for _, address := range []string{"ldap://127.0.0.1:1", "ldaps://127.0.0.1:1"} {
		dead := ldapTestClient(LDAPOptions{URL: address, TimeoutSeconds: 1})
		if _, err := dead.Authenticate(context.Background(), "alice", "secret"); err == nil {
			t.Fatalf("unreachable directory %s must fail", address)
		}
	}
}
