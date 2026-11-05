package identity

// AuthOptions is the authentication tier owned by this package. The composition
// root maps deployment configuration onto it, so identity never reads the
// global config object.
type AuthOptions struct {
	SessionTTLSeconds int
	JWTIssuer         string
	JWTAudience       string
	SigningKeys       []string
	ActiveKeyID       string
	DevAuthEnabled    bool
	APIKeyPepper      string
	OIDC              OIDCOptions
}

type OIDCOptions struct {
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
