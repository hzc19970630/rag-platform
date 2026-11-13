package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// AuthConfig holds authentication policy. Signing keys are a ring so a key can
// be rotated without invalidating every live session: the active key signs new
// tokens, the old keys verify tokens issued before the rotation until they
// expire.
type AuthConfig struct {
	SessionTTLSeconds int
	JWTIssuer         string
	JWTAudience       string
	SigningKeys       []string
	// SigningKeysExplicit records whether JWT_SIGNING_KEYS was provided. The
	// development fallback below is rejected in production, where a single
	// shared default key must never sign sessions.
	SigningKeysExplicit bool
	ActiveKeyID         string
	CookieName          string
	CookieSecure        bool
	DevAuthEnabled      bool
	APIKeyPepper        string
	OIDC                OIDCConfig
	LDAP                LDAPConfig
}

// LDAPConfig describes one directory-backed identity source. The deployment
// binds with a read-only service account for searches and rebinds as the user
// to verify the password; user passwords are never persisted.
type LDAPConfig struct {
	Enabled             bool
	URL                 string
	StartTLS            bool
	TLSSkipVerify       bool
	AllowInsecure       bool
	BindDN              string
	BindPassword        string
	BaseDN              string
	UserFilter          string
	UserIDAttribute     string
	EmailAttribute      string
	NameAttribute       string
	GroupAttribute      string
	GroupFilter         string
	TenantID            string
	AutoProvision       bool
	DefaultRole         string
	GroupRoleMap        string
	SyncUserFilter      string
	SyncIntervalSeconds int
	TimeoutSeconds      int
}

type OIDCConfig struct {
	Enabled       bool
	Issuer        string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	Scopes        []string
	TenantClaim   string
	EmailClaim    string
	NameClaim     string
	AutoProvision bool
	DefaultTenant string
}

func (a AuthConfig) Validate(appEnv string) error {
	if len(a.SigningKeys) == 0 {
		return fmt.Errorf("JWT_SIGNING_KEYS must contain at least one kid:secret entry")
	}
	active := false
	for _, entry := range a.SigningKeys {
		kid, secret, ok := strings.Cut(entry, ":")
		if !ok || strings.TrimSpace(kid) == "" || len(secret) < 16 {
			return fmt.Errorf("each JWT_SIGNING_KEYS entry must be kid:secret with a secret of at least 16 characters")
		}
		if kid == a.ActiveKeyID {
			active = true
		}
	}
	if !active {
		return fmt.Errorf("JWT_ACTIVE_KID must name one of JWT_SIGNING_KEYS")
	}
	if a.SessionTTLSeconds <= 0 {
		return fmt.Errorf("AUTH_SESSION_TTL_SECONDS must be positive")
	}
	if strings.TrimSpace(a.APIKeyPepper) == "" {
		return fmt.Errorf("API_KEY_PEPPER must not be empty")
	}
	if a.OIDC.Enabled {
		if strings.TrimSpace(a.OIDC.Issuer) == "" || strings.TrimSpace(a.OIDC.ClientID) == "" || strings.TrimSpace(a.OIDC.RedirectURL) == "" {
			return fmt.Errorf("OIDC_ISSUER, OIDC_CLIENT_ID and OIDC_REDIRECT_URL are required when OIDC_ENABLED=true")
		}
		parsed, err := url.Parse(a.OIDC.Issuer)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("OIDC_ISSUER must be a valid URL")
		}
	}
	if a.LDAP.Enabled {
		parsed, err := url.Parse(a.LDAP.URL)
		if err != nil || (parsed.Scheme != "ldap" && parsed.Scheme != "ldaps") || parsed.Host == "" {
			return fmt.Errorf("LDAP_URL must be ldap:// or ldaps://host:port")
		}
		if strings.TrimSpace(a.LDAP.BindDN) == "" || strings.TrimSpace(a.LDAP.BindPassword) == "" || strings.TrimSpace(a.LDAP.BaseDN) == "" {
			return fmt.Errorf("LDAP_BIND_DN, LDAP_BIND_PASSWORD and LDAP_BASE_DN are required when LDAP_ENABLED=true")
		}
		if strings.TrimSpace(a.LDAP.TenantID) == "" {
			return fmt.Errorf("LDAP_TENANT_ID is required when LDAP_ENABLED=true")
		}
		if !strings.Contains(a.LDAP.UserFilter, "%s") {
			return fmt.Errorf("LDAP_USER_FILTER must contain %%s for the escaped username")
		}
		if strings.TrimSpace(a.LDAP.GroupFilter) != "" && !strings.Contains(a.LDAP.GroupFilter, "%s") {
			return fmt.Errorf("LDAP_GROUP_FILTER must contain %%s for the escaped user DN")
		}
		if a.LDAP.DefaultRole != "" && !validTenantRoleName(a.LDAP.DefaultRole) {
			return fmt.Errorf("LDAP_DEFAULT_ROLE must be one of owner, admin, member, viewer")
		}
		if strings.TrimSpace(a.LDAP.SyncUserFilter) == "" {
			return fmt.Errorf("LDAP_SYNC_USER_FILTER is required when LDAP_ENABLED=true")
		}
		for _, pair := range strings.Split(a.LDAP.GroupRoleMap, ";") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			separator := strings.LastIndex(pair, "=")
			if separator <= 0 || separator == len(pair)-1 ||
				strings.TrimSpace(pair[:separator]) == "" || !validTenantRoleName(strings.TrimSpace(pair[separator+1:])) {
				return fmt.Errorf("LDAP_GROUP_ROLE_MAP entries must be group=role with role in owner/admin/member/viewer")
			}
		}
		if appEnv == "production" && parsed.Scheme == "ldap" && !a.LDAP.StartTLS && !a.LDAP.AllowInsecure {
			return fmt.Errorf("production LDAP must use ldaps://, LDAP_START_TLS=true, or an explicit LDAP_ALLOW_INSECURE=true acknowledgement")
		}
	}
	if appEnv == "production" {
		if !a.SigningKeysExplicit {
			return fmt.Errorf("JWT_SIGNING_KEYS must be set explicitly in production")
		}
		for _, entry := range a.SigningKeys {
			kid, secret, _ := strings.Cut(entry, ":")
			if isRepositoryDefaultSecret(secret) {
				return fmt.Errorf("JWT_SIGNING_KEYS entry %q uses a repository default secret in production", kid)
			}
		}
		if isRepositoryDefaultSecret(a.APIKeyPepper) {
			return fmt.Errorf("API_KEY_PEPPER must not use a repository default value in production")
		}
		if !a.OIDC.Enabled && !a.LDAP.Enabled {
			return fmt.Errorf("OIDC_ENABLED or LDAP_ENABLED must be true in production")
		}
		if a.DevAuthEnabled {
			return fmt.Errorf("DEV_AUTH_ENABLED must be false in production")
		}
		if !a.CookieSecure {
			return fmt.Errorf("AUTH_COOKIE_SECURE must be true in production")
		}
		if a.OIDC.Enabled && a.OIDC.ClientSecret == "" {
			return fmt.Errorf("OIDC_CLIENT_SECRET is required in production")
		}
		for _, entry := range a.SigningKeys {
			_, secret, _ := strings.Cut(entry, ":")
			if len(secret) < 32 {
				return fmt.Errorf("JWT signing secrets must be at least 32 characters in production")
			}
		}
	}
	return nil
}

// validateTrustedProxies accepts the same forms gin does: a bare IP or a CIDR.
// Invalid entries fail startup rather than silently leaving forwarding headers
// untrusted, which would attribute every request to the proxy.
func validateTrustedProxies(entries []string) error {
	for _, entry := range entries {
		if strings.Contains(entry, "/") {
			_, network, err := net.ParseCIDR(entry)
			if err != nil {
				return fmt.Errorf("HTTP_TRUSTED_PROXIES entry %q is not a valid CIDR", entry)
			}
			if ones, _ := network.Mask.Size(); ones == 0 {
				return fmt.Errorf("HTTP_TRUSTED_PROXIES entry %q trusts every peer; list the proxy network instead", entry)
			}
			continue
		}
		if net.ParseIP(entry) == nil {
			return fmt.Errorf("HTTP_TRUSTED_PROXIES entry %q is not a valid IP address", entry)
		}
	}
	return nil
}

func validTenantRoleName(role string) bool {
	switch role {
	case "owner", "admin", "member", "viewer":
		return true
	default:
		return false
	}
}
