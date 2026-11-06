package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type Lookup func(string) (string, bool)

func value(lookup Lookup, key, fallback string) string {
	if current, ok := lookup(key); ok && current != "" {
		return current
	}
	return fallback
}

// databaseURLRole returns the login role from a database URL. Enforce mode
// checks it here so a swapped role fails before any connection is opened; the
// role's actual RLS attributes are still verified against pg_roles at startup.
func databaseURLRole(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("must be a valid database URL")
	}
	if parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return "", fmt.Errorf("must name a login role")
	}
	return parsed.User.Username(), nil
}

// databaseURLPassword extracts the password so production can reject the
// repository defaults without logging the value. An empty password is left to
// the deployment (certificate or peer authentication remain valid).
func databaseURLPassword(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("must be a valid database URL")
	}
	if parsed.User == nil {
		return "", nil
	}
	password, _ := parsed.User.Password()
	return password, nil
}

// repositoryDefaultSecrets are the values shipped in .env.example or compiled
// into development fallbacks. Production must not sign, encrypt or connect with
// any of them.
var repositoryDefaultSecrets = map[string]struct{}{
	"local-development-signing-key-change-me":        {},
	"replace-with-a-random-secret-at-least-32-bytes": {},
	"local-api-key-pepper-change-me":                 {},
	"replace-with-a-random-api-key-pepper":           {},
	"rag-local":                                      {},
	"change-me-local-only":                           {},
	"change-me-app":                                  {},
	"change-me-maintenance":                          {},
}

func isRepositoryDefaultSecret(value string) bool {
	_, found := repositoryDefaultSecrets[strings.ToLower(strings.TrimSpace(value))]
	return found
}

// AdminDBURL is the connection migrate, seed and embedctl use. Without a split
// role it stays on DATABASE_URL, which is the only mode where DDL and runtime
// traffic sharing one owner role is acceptable.
func (c Config) AdminDBURL() string {
	if admin := strings.TrimSpace(c.AdminDatabaseURL); admin != "" {
		return admin
	}
	return c.DatabaseURL
}

// envReader parses typed settings and records every malformed value. Falling
// back silently would make a typo indistinguishable from an unset variable:
// the operator would see the default in effect while believing the configured
// value was applied.
type envReader struct {
	lookup Lookup
	err    error
}

func (r *envReader) failf(format string, args ...any) {
	r.err = errors.Join(r.err, fmt.Errorf(format, args...))
}

func (r *envReader) raw(key string) string {
	current, ok := r.lookup(key)
	if !ok {
		return ""
	}
	return strings.TrimSpace(current)
}

func (r *envReader) boolean(key string, fallback bool) bool {
	current := r.raw(key)
	if current == "" {
		return fallback
	}
	switch strings.ToLower(current) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		r.failf("%s must be a boolean (true/false), got %q", key, current)
		return fallback
	}
}

// splitValue reads a comma-separated list without marker unescaping. Signing
// secrets and OIDC scopes must be taken literally.
func splitValue(lookup Lookup, key string, fallback []string) []string {
	current, ok := lookup(key)
	if !ok || strings.TrimSpace(current) == "" {
		return fallback
	}
	parts := strings.Split(current, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			result = append(result, item)
		}
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}

// validateModelList accepts entries in name or name@version form. The version
// is recorded in binding audits so an in-place provider upgrade is visible.
func validateModelList(key string, entries []string) error {
	for _, entry := range entries {
		name, version, hasVersion := strings.Cut(entry, "@")
		if strings.TrimSpace(name) == "" || (hasVersion && strings.TrimSpace(version) == "") {
			return fmt.Errorf("%s entries must be model or model@version, got %q", key, entry)
		}
	}
	return nil
}

// positive reads an integer that must be strictly greater than zero.
func (r *envReader) positive(key string, fallback int64) int64 {
	current := r.raw(key)
	if current == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(current, 10, 64)
	if err != nil || parsed <= 0 {
		r.failf("%s must be a positive integer, got %q", key, current)
		return fallback
	}
	return parsed
}

func listValue(lookup Lookup, key string, fallback []string) []string {
	current, ok := lookup(key)
	if !ok || strings.TrimSpace(current) == "" {
		return fallback
	}
	parts := strings.Split(current, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		// Markers may legitimately contain leading or trailing whitespace, so
		// only the surrounding comma padding is removed here. Unescaping happens
		// after trimming, otherwise a "\n" marker would be trimmed to nothing.
		item := unescapeMarkers(strings.TrimSpace(part))
		if item != "" {
			result = append(result, item)
		}
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}

func (r *envReader) rate(key string, fallback float64) float64 {
	current := r.raw(key)
	if current == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(current, 64)
	if err != nil || parsed <= 0 {
		r.failf("%s must be a positive number, got %q", key, current)
		return fallback
	}
	return parsed
}

// nonNegative reads an integer that is allowed to be zero. Overlap, minimum
// size and retention windows are meaningful at zero, so they need their own
// parser.
func (r *envReader) nonNegative(key string, fallback int64) int64 {
	current := r.raw(key)
	if current == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(current, 10, 64)
	if err != nil || parsed < 0 {
		r.failf("%s must be a non-negative integer, got %q", key, current)
		return fallback
	}
	return parsed
}

// unescapeMarkers lets a marker list be written in .env as "\n\n,\n"
// where a literal newline would be awkward to express.
func unescapeMarkers(value string) string {
	value = strings.ReplaceAll(value, "\\r", "\r")
	value = strings.ReplaceAll(value, "\\t", "\t")
	return strings.ReplaceAll(value, "\\n", "\n")
}
