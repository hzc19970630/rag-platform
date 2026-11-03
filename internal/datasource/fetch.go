package datasource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxRedirects    = 3
	maxManifestItem = 10000
	maxRetry        = 3
)

// Credentials is the plaintext shape of a connector credential. It only ever
// exists inside the API process (while sealing) and the worker process (while
// fetching). The database stores an AES-GCM envelope instead.
type Credentials struct {
	Type     string `json:"type"`
	Token    string `json:"token,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type ManifestItem struct {
	ExternalID string `json:"id"`
	URL        string `json:"url"`
	// Path is an optional filename/permission path hint. The filename from Path
	// (or the URL basename) determines the ingestion type by extension.
	Path string `json:"path,omitempty"`
	// Version is the remote change token. When present and unchanged, the item
	// is skipped without downloading it.
	Version string `json:"version,omitempty"`
	// SHA256 is an optional expected content digest. It is verified after
	// download and also serves as a change token.
	SHA256 string `json:"sha256,omitempty"`
}

type Manifest struct {
	Items []ManifestItem `json:"items"`
}

type RemoteFile struct {
	Content     []byte
	ContentHash string
	ContentType string
}

// RetryableError marks a remote failure that a later attempt may resolve
// (network error, timeout, 429 or 5xx). The sync worker leaves the run active
// and lets Asynq retry instead of recording a per-item failure.
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string { return e.Err.Error() }

func (e *RetryableError) Unwrap() error { return e.Err }

func IsRetryable(err error) bool {
	var retryable *RetryableError
	return errors.As(err, &retryable)
}

// FetchOptions carries the operator-configured guardrails. Every value is
// applied per request, so a connector cannot widen its own limits.
type FetchOptions struct {
	AllowPrivateNetworks     bool
	Timeout                  time.Duration
	MaxManifestBytes         int64
	MaxFileBytes             int64
	DefaultRequestsPerSecond float64
}

// Fetcher performs connector HTTP reads behind an SSRF guard, a per-connector
// rate limiter, bounded bodies and bounded retries. Credentials are attached
// only to same-scheme, same-host requests, so a manifest that points at a
// third-party host cannot exfiltrate the connector secret.
type Fetcher struct {
	client       *http.Client
	allowPrivate bool
	maxManifest  int64
	maxFile      int64
	defaultRate  float64

	mu       sync.Mutex
	limiters map[string]*rateLimiter
}

func NewFetcher(options FetchOptions) *Fetcher {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	maxManifest := options.MaxManifestBytes
	if maxManifest <= 0 {
		maxManifest = 2 * 1024 * 1024
	}
	maxFile := options.MaxFileBytes
	if maxFile <= 0 {
		maxFile = 100 * 1024 * 1024
	}
	defaultRate := options.DefaultRequestsPerSecond
	if defaultRate <= 0 {
		defaultRate = 2
	}
	guard := &ssrfGuard{allowPrivate: options.AllowPrivateNetworks}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           guard.dialContext(dialer),
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          20,
		IdleConnTimeout:       60 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("too many redirects")
			}
			origin := via[0].URL
			if !sameHost(origin, request.URL) {
				return fmt.Errorf("cross-host redirect rejected")
			}
			if origin.Scheme == "https" && request.URL.Scheme != "https" {
				return fmt.Errorf("cross-scheme redirect rejected")
			}
			if request.URL.Scheme != "http" && request.URL.Scheme != "https" {
				return fmt.Errorf("redirect scheme must be http or https")
			}
			return nil
		},
	}
	return &Fetcher{client: client, allowPrivate: options.AllowPrivateNetworks, maxManifest: maxManifest, maxFile: maxFile, defaultRate: defaultRate, limiters: map[string]*rateLimiter{}}
}

// ParseManifestURL validates the configured entry point. Item URLs may be
// relative to it.
func ParseManifestURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("manifest URL is invalid: unsupported URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported manifest URL scheme")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("manifest URL host is required")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("manifest URL credentials are unsupported")
	}
	return parsed, nil
}

// FetchManifest downloads and normalizes the manifest. The returned hash is a
// canonical digest over the sorted item list: it changes when any item's
// identity, URL, version or checksum changes, regardless of JSON ordering. An
// empty list is a valid snapshot: the source currently has no items, so every
// previously synced item becomes a tombstone.
func (f *Fetcher) FetchManifest(ctx context.Context, connectorID string, manifestURL *url.URL, credential *Credentials, requestsPerSecond float64) (Manifest, string, error) {
	body, _, err := f.get(ctx, connectorID, manifestURL.String(), manifestURL, credential, requestsPerSecond, "application/json", f.maxManifest)
	if err != nil {
		return Manifest{}, "", fmt.Errorf("fetch manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return Manifest{}, "", fmt.Errorf("decode manifest: %w", err)
	}
	if len(manifest.Items) > maxManifestItem {
		return Manifest{}, "", fmt.Errorf("manifest contains more than %d items", maxManifestItem)
	}
	seen := make(map[string]struct{}, len(manifest.Items))
	for index := range manifest.Items {
		item := &manifest.Items[index]
		item.ExternalID = strings.TrimSpace(item.ExternalID)
		if item.ExternalID == "" {
			return Manifest{}, "", fmt.Errorf("manifest item %d has no id", index)
		}
		if len(item.ExternalID) > 512 {
			return Manifest{}, "", fmt.Errorf("manifest item id exceeds 512 characters")
		}
		if _, exists := seen[item.ExternalID]; exists {
			return Manifest{}, "", fmt.Errorf("manifest contains duplicate item id %q", item.ExternalID)
		}
		seen[item.ExternalID] = struct{}{}
		itemURL, err := resolveItemURL(manifestURL, item.URL)
		if err != nil {
			return Manifest{}, "", fmt.Errorf("manifest item %q: %w", item.ExternalID, err)
		}
		item.URL = itemURL
		item.Version = strings.TrimSpace(item.Version)
		if len(item.Version) > 255 {
			return Manifest{}, "", fmt.Errorf("manifest item %q version exceeds 255 characters", item.ExternalID)
		}
		item.SHA256 = strings.ToLower(strings.TrimSpace(item.SHA256))
		if item.SHA256 != "" && !isHexDigest(item.SHA256) {
			return Manifest{}, "", fmt.Errorf("manifest item %q has an invalid sha256", item.ExternalID)
		}
		if len(item.Path) > 1024 {
			return Manifest{}, "", fmt.Errorf("manifest item %q path exceeds 1024 characters", item.ExternalID)
		}
	}
	return manifest, manifestHash(manifest), nil
}

func (f *Fetcher) FetchFile(ctx context.Context, connectorID string, item ManifestItem, manifestURL *url.URL, credential *Credentials, requestsPerSecond float64) (RemoteFile, error) {
	itemURL, err := resolveItemURL(manifestURL, item.URL)
	if err != nil {
		return RemoteFile{}, fmt.Errorf("manifest item %q: %w", item.ExternalID, err)
	}
	body, contentType, err := f.get(ctx, connectorID, itemURL, manifestURL, credential, requestsPerSecond, "*/*", f.maxFile)
	if err != nil {
		return RemoteFile{}, fmt.Errorf("fetch manifest item %q: %w", item.ExternalID, err)
	}
	digest := sha256.Sum256(body)
	contentHash := hex.EncodeToString(digest[:])
	if item.SHA256 != "" && contentHash != item.SHA256 {
		return RemoteFile{}, fmt.Errorf("manifest item %q checksum mismatch", item.ExternalID)
	}
	return RemoteFile{Content: body, ContentHash: contentHash, ContentType: contentType}, nil
}

// ItemFilename derives a safe basename. Directory components in a remote path
// are discarded so a manifest cannot influence the object key layout.
func ItemFilename(item ManifestItem) string {
	candidate := strings.TrimSpace(item.Path)
	if candidate == "" {
		if parsed, err := url.Parse(item.URL); err == nil {
			candidate = parsed.Path
		}
	}
	name := path.Base(strings.ReplaceAll(candidate, "\\", "/"))
	if name == "." || name == "/" {
		return ""
	}
	return name
}

func (f *Fetcher) get(ctx context.Context, connectorID, rawURL string, credentialOrigin *url.URL, credential *Credentials, requestsPerSecond float64, accept string, maxBytes int64) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, "", fmt.Errorf("remote URL is invalid")
	}
	if maxBytes <= 0 {
		maxBytes = f.maxManifest
	}
	limiter := f.limiter(connectorID, requestsPerSecond)
	var lastErr error
	for attempt := 0; attempt < maxRetry; attempt++ {
		if err := limiter.wait(ctx); err != nil {
			return nil, "", err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return nil, "", fmt.Errorf("build remote request: %w", err)
		}
		request.Header.Set("Accept", accept)
		request.Header.Set("User-Agent", "rag-platform-connector/1.0")
		if credential != nil && credential.Type != "" && sameOrigin(credentialOrigin, parsed) {
			switch credential.Type {
			case "bearer":
				request.Header.Set("Authorization", "Bearer "+credential.Token)
			case "basic":
				request.SetBasicAuth(credential.Username, credential.Password)
			}
		}
		response, err := f.client.Do(request)
		if err != nil {
			lastErr = fmt.Errorf("remote request failed: %w", err)
			if attempt+1 < maxRetry {
				if err := sleepContext(ctx, retryDelay(attempt, "")); err != nil {
					return nil, "", err
				}
				continue
			}
			break
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
		closeErr := response.Body.Close()
		if readErr != nil {
			return nil, "", &RetryableError{Err: fmt.Errorf("read remote response: %w", readErr)}
		}
		if closeErr != nil {
			return nil, "", &RetryableError{Err: fmt.Errorf("close remote response: %w", closeErr)}
		}
		if int64(len(body)) > maxBytes {
			return nil, "", fmt.Errorf("remote response exceeds the configured size limit")
		}
		if response.StatusCode == http.StatusOK {
			return body, response.Header.Get("Content-Type"), nil
		}
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			lastErr = fmt.Errorf("remote returned HTTP %d", response.StatusCode)
			if attempt+1 < maxRetry {
				if err := sleepContext(ctx, retryDelay(attempt, response.Header.Get("Retry-After"))); err != nil {
					return nil, "", err
				}
				continue
			}
			break
		}
		// Client errors are final and the body may contain provider details, so
		// only the status is reported.
		return nil, "", fmt.Errorf("remote returned HTTP %d", response.StatusCode)
	}
	return nil, "", &RetryableError{Err: lastErr}
}

func retryDelay(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds > 0 {
		if seconds > 30 {
			seconds = 30
		}
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(1<<attempt) * 500 * time.Millisecond
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (f *Fetcher) limiter(connectorID string, requestsPerSecond float64) *rateLimiter {
	if requestsPerSecond <= 0 {
		requestsPerSecond = f.defaultRate
	}
	interval := time.Duration(float64(time.Second) / requestsPerSecond)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	limiter, ok := f.limiters[connectorID]
	// The configured rate can change through PATCH. Replacing the limiter keeps
	// the new rate effective on the next request instead of after a restart.
	if !ok || limiter.interval != interval {
		limiter = &rateLimiter{interval: interval}
		f.limiters[connectorID] = limiter
	}
	return limiter
}

// rateLimiter spaces requests per connector. It is intentionally not a burst
// bucket: remote sources are usually rate-limited over a window, and steady
// spacing keeps the connector inside that window.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func (r *rateLimiter) wait(ctx context.Context) error {
	r.mu.Lock()
	now := time.Now()
	if r.next.Before(now) {
		r.next = now
	}
	wait := r.next.Sub(now)
	r.next = r.next.Add(r.interval)
	r.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	return sleepContext(ctx, wait)
}

// ssrfGuard validates the address that will actually be dialed. Resolving name
// to addresses and then dialing one of those addresses prevents a DNS-rebinding
// host from passing validation with a public IP and then connecting to a
// private one.
type ssrfGuard struct {
	allowPrivate bool
}

func (g *ssrfGuard) dialContext(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("remote address is invalid")
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve remote host: %w", err)
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("resolve remote host: no addresses")
		}
		var lastErr error
		for _, candidate := range addresses {
			if !g.allowPrivate && blockedIP(candidate.IP) {
				lastErr = fmt.Errorf("remote host resolves to a private or reserved address")
				continue
			}
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("remote host is unreachable")
		}
		return nil, lastErr
	}
}

func blockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast()
}

func resolveItemURL(manifestURL *url.URL, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("url is required")
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("url is invalid")
	}
	resolved := manifestURL.ResolveReference(reference)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", fmt.Errorf("url must use http or https")
	}
	if resolved.Host == "" {
		return "", fmt.Errorf("url must include a host")
	}
	if resolved.User != nil {
		return "", fmt.Errorf("url must not contain credentials")
	}
	return resolved.String(), nil
}

func sameHost(left, right *url.URL) bool {
	return strings.EqualFold(left.Host, right.Host)
}

// sameOrigin scopes credentials to the exact scheme and host they were
// configured for. Host equality alone would let an http item URL on an
// https-configured manifest receive the credential in cleartext.
func sameOrigin(origin, target *url.URL) bool {
	if origin == nil || target == nil {
		return false
	}
	return strings.EqualFold(origin.Scheme, target.Scheme) && strings.EqualFold(origin.Host, target.Host)
}

func manifestHash(manifest Manifest) string {
	canonical := make([]ManifestItem, len(manifest.Items))
	copy(canonical, manifest.Items)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].ExternalID < canonical[j].ExternalID })
	encoded, _ := json.Marshal(canonical)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func isHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
