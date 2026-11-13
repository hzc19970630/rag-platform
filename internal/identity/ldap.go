package identity

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/hzc19970630/rag-platform/internal/apperror"
)

// LDAPIdentity is one directory user after bind and group lookup. The password
// is consumed by the bind and never returned or stored.
type LDAPIdentity struct {
	DN       string
	Username string
	Email    string
	Name     string
	Groups   []string
	TenantID string
}

// LDAPDirectory is the narrow port the identity service depends on. Tests can
// replace it without standing up a directory.
type LDAPDirectory interface {
	Authenticate(ctx context.Context, username, password string) (LDAPIdentity, error)
	ListUsers(ctx context.Context) ([]LDAPIdentity, error)
	RoleForGroups(groups []string) (string, bool)
}

type LDAPClient struct {
	cfg         LDAPOptions
	roleByGroup map[string]string
	roleOrder   map[string]int
}

func NewLDAPClient(cfg LDAPOptions) *LDAPClient {
	return &LDAPClient{
		cfg:         cfg,
		roleByGroup: parseGroupRoleMap(cfg.GroupRoleMap),
		roleOrder:   map[string]int{"owner": 4, "admin": 3, "member": 2, "viewer": 1},
	}
}

func (c *LDAPClient) Enabled() bool { return c != nil && c.cfg.Enabled }

// Authenticate performs a service-account search, a user bind with the supplied
// password, then reads the user's groups. A missing user and a wrong password
// both collapse to ErrUnauthenticated so callers cannot enumerate accounts.
func (c *LDAPClient) Authenticate(ctx context.Context, username, password string) (LDAPIdentity, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return LDAPIdentity{}, apperror.ErrUnauthenticated
	}
	conn, err := c.connect(ctx)
	if err != nil {
		return LDAPIdentity{}, err
	}
	defer conn.Close()

	entry, err := c.searchUser(conn, username)
	if err != nil {
		return LDAPIdentity{}, err
	}
	// Read groups as the service account before rebinding: a normal user often
	// cannot read group entries, while the password must still be verified by
	// the user bind below.
	groups, err := c.groupsFor(conn, entry)
	if err != nil {
		return LDAPIdentity{}, err
	}
	if err := conn.Bind(entry.DN, password); err != nil {
		var ldapErr *ldap.Error
		if errors.As(err, &ldapErr) && ldapErr.ResultCode == ldap.LDAPResultInvalidCredentials {
			return LDAPIdentity{}, apperror.ErrUnauthenticated
		}
		return LDAPIdentity{}, fmt.Errorf("ldap user bind: %w", err)
	}
	return c.identityFromEntry(entry, groups), nil
}

// ListUsers returns every directory user matching LDAP_SYNC_USER_FILTER. It is
// used by the periodic organization sync; group membership is read for each
// user through the same path as login.
func (c *LDAPClient) ListUsers(ctx context.Context) ([]LDAPIdentity, error) {
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	request := ldap.NewSearchRequest(
		c.cfg.BaseDN,
		ldap.ScopeWholeSubtree, ldap.DerefAlways, 0, 0, false,
		c.cfg.SyncUserFilter,
		c.attributes(),
		nil,
	)
	entries, err := conn.Search(request)
	if err != nil {
		return nil, fmt.Errorf("ldap list users: %w", err)
	}
	users := make([]LDAPIdentity, 0, len(entries.Entries))
	for _, entry := range entries.Entries {
		groups, err := c.groupsFor(conn, entry)
		if err != nil {
			return nil, err
		}
		users = append(users, c.identityFromEntry(entry, groups))
	}
	return users, nil
}

// RoleForGroups returns the highest-privilege mapped role. The second result is
// false when no group matches and no default role is configured.
func (c *LDAPClient) RoleForGroups(groups []string) (string, bool) {
	best := ""
	bestRank := 0
	for _, group := range groups {
		role, ok := c.roleForGroup(group)
		if !ok {
			continue
		}
		rank := c.roleOrder[role]
		if rank > bestRank {
			best = role
			bestRank = rank
		}
	}
	if best != "" {
		return best, true
	}
	if role := strings.TrimSpace(c.cfg.DefaultRole); role != "" {
		return role, true
	}
	return "", false
}

func (c *LDAPClient) roleForGroup(group string) (string, bool) {
	normalized := normalizeDN(group)
	if role, ok := c.roleByGroup[normalized]; ok {
		return role, true
	}
	// Allow matching by CN as well as the full DN: AD returns DNs while some
	// OpenLDAP fixtures use groupOfNames with a simple cn.
	if cn := commonName(group); cn != "" {
		if role, ok := c.roleByGroup[normalizeDN(cn)]; ok {
			return role, true
		}
	}
	return "", false
}

func (c *LDAPClient) connect(ctx context.Context) (*ldap.Conn, error) {
	parsed, err := url.Parse(c.cfg.URL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("ldap url is invalid")
	}
	dialer := &net.Dialer{Timeout: time.Duration(c.cfg.TimeoutSeconds) * time.Second}
	options := []ldap.DialOpt{ldap.DialWithDialer(dialer)}
	if parsed.Scheme == "ldaps" {
		options = append(options, ldap.DialWithTLSConfig(&tls.Config{
			ServerName:         parsed.Hostname(),
			InsecureSkipVerify: c.cfg.TLSSkipVerify,
			MinVersion:         tls.VersionTLS12,
		}))
	}
	conn, err := ldap.DialURL(c.cfg.URL, options...)
	if err != nil {
		return nil, fmt.Errorf("ldap connect: %w", err)
	}
	if c.cfg.StartTLS {
		if err := conn.StartTLS(&tls.Config{
			ServerName:         parsed.Hostname(),
			InsecureSkipVerify: c.cfg.TLSSkipVerify,
			MinVersion:         tls.VersionTLS12,
		}); err != nil {
			conn.Close()
			return nil, fmt.Errorf("ldap starttls: %w", err)
		}
	}
	if err := conn.Bind(c.cfg.BindDN, c.cfg.BindPassword); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ldap service bind: %w", err)
	}
	return conn, nil
}

func (c *LDAPClient) searchUser(conn *ldap.Conn, username string) (*ldap.Entry, error) {
	request := ldap.NewSearchRequest(
		c.cfg.BaseDN,
		ldap.ScopeWholeSubtree, ldap.DerefAlways, 2, 0, false,
		fmt.Sprintf(c.cfg.UserFilter, ldap.EscapeFilter(username)),
		c.attributes(),
		nil,
	)
	result, err := conn.Search(request)
	if err != nil {
		return nil, fmt.Errorf("ldap search user: %w", err)
	}
	if len(result.Entries) != 1 {
		return nil, apperror.ErrUnauthenticated
	}
	return result.Entries[0], nil
}

func (c *LDAPClient) groupsFor(conn *ldap.Conn, entry *ldap.Entry) ([]string, error) {
	if strings.TrimSpace(c.cfg.GroupFilter) == "" {
		return uniqueStrings(entry.GetAttributeValues(c.cfg.GroupAttribute)), nil
	}
	request := ldap.NewSearchRequest(
		c.cfg.BaseDN,
		ldap.ScopeWholeSubtree, ldap.DerefAlways, 0, 0, false,
		fmt.Sprintf(c.cfg.GroupFilter, ldap.EscapeFilter(entry.DN)),
		[]string{"dn", "cn", c.cfg.GroupAttribute},
		nil,
	)
	result, err := conn.Search(request)
	if err != nil {
		return nil, fmt.Errorf("ldap search groups: %w", err)
	}
	groups := make([]string, 0, len(result.Entries))
	for _, group := range result.Entries {
		groups = append(groups, group.DN)
		if cn := group.GetAttributeValue("cn"); cn != "" {
			groups = append(groups, cn)
		}
	}
	return uniqueStrings(groups), nil
}

func (c *LDAPClient) identityFromEntry(entry *ldap.Entry, groups []string) LDAPIdentity {
	username := entry.GetAttributeValue(c.cfg.UserIDAttribute)
	if username == "" {
		username = entry.GetAttributeValue("uid")
	}
	return LDAPIdentity{
		DN:       entry.DN,
		Username: username,
		Email:    entry.GetAttributeValue(c.cfg.EmailAttribute),
		Name:     entry.GetAttributeValue(c.cfg.NameAttribute),
		Groups:   groups,
		TenantID: c.cfg.TenantID,
	}
}

func (c *LDAPClient) attributes() []string {
	attrs := []string{c.cfg.UserIDAttribute, c.cfg.EmailAttribute, c.cfg.NameAttribute}
	if strings.TrimSpace(c.cfg.GroupFilter) == "" && c.cfg.GroupAttribute != "" {
		attrs = append(attrs, c.cfg.GroupAttribute)
	}
	return uniqueStrings(attrs)
}

func parseGroupRoleMap(raw string) map[string]string {
	result := map[string]string{}
	for _, pair := range strings.Split(raw, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		separator := strings.LastIndex(pair, "=")
		if separator <= 0 || separator == len(pair)-1 {
			continue
		}
		group := strings.TrimSpace(pair[:separator])
		role := strings.ToLower(strings.TrimSpace(pair[separator+1:]))
		if group == "" || role == "" {
			continue
		}
		result[normalizeDN(group)] = role
	}
	return result
}

func normalizeDN(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func commonName(group string) string {
	for _, part := range strings.Split(group, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "cn") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
