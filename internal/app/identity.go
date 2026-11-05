package app

import (
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/identity"
)

// identityOptions maps deployment configuration onto the identity package's own
// options type, keeping the domain free of the global config struct.
func identityOptions(cfg config.AuthConfig) identity.AuthOptions {
	return identity.AuthOptions{
		SessionTTLSeconds: cfg.SessionTTLSeconds,
		JWTIssuer:         cfg.JWTIssuer,
		JWTAudience:       cfg.JWTAudience,
		SigningKeys:       cfg.SigningKeys,
		ActiveKeyID:       cfg.ActiveKeyID,
		DevAuthEnabled:    cfg.DevAuthEnabled,
		APIKeyPepper:      cfg.APIKeyPepper,
		OIDC: identity.OIDCOptions{
			Enabled:       cfg.OIDC.Enabled,
			Issuer:        cfg.OIDC.Issuer,
			ClientID:      cfg.OIDC.ClientID,
			ClientSecret:  cfg.OIDC.ClientSecret,
			RedirectURL:   cfg.OIDC.RedirectURL,
			Scopes:        cfg.OIDC.Scopes,
			TenantClaim:   cfg.OIDC.TenantClaim,
			EmailClaim:    cfg.OIDC.EmailClaim,
			NameClaim:     cfg.OIDC.NameClaim,
			AutoProvision: cfg.OIDC.AutoProvision,
			DefaultTenant: cfg.OIDC.DefaultTenant,
		},
	}
}
