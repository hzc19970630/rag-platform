package datasource

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testFetcher(allowPrivate bool) *Fetcher {
	return NewFetcher(FetchOptions{
		AllowPrivateNetworks: allowPrivate, Timeout: 3 * time.Second,
		MaxManifestBytes: 1 << 20, MaxFileBytes: 1 << 20, DefaultRequestsPerSecond: 50,
	})
}

func manifestServer(t *testing.T, items []map[string]any) (*httptest.Server, *url.URL) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	}))
	t.Cleanup(server.Close)
	parsed, err := ParseManifestURL(server.URL + "/manifest.json")
	if err != nil {
		t.Fatalf("parse manifest url: %v", err)
	}
	return server, parsed
}

func TestParseManifestURLRejectsUnsafeShapes(t *testing.T) {
	cases := []string{"", "ftp://example.com/m.json", "https://user:pass@example.com/m.json", "/relative/manifest.json", "https:///missing-host"}
	for _, raw := range cases {
		if _, err := ParseManifestURL(raw); err == nil {
			t.Fatalf("expected %q to be rejected", raw)
		}
	}
	if _, err := ParseManifestURL("https://example.com/manifest.json"); err != nil {
		t.Fatalf("valid manifest url rejected: %v", err)
	}
}

func TestFetchManifestNormalizesRelativeItemsAndHash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
			{"id": "a", "url": "files/a.md", "path": "docs/a.md", "version": "1"},
			{"id": "b", "url": "http://" + r.Host + "/b.txt", "sha256": strings.Repeat("a", 64)},
		}})
	}))
	defer server.Close()
	manifestURL, err := ParseManifestURL(server.URL + "/manifest.json")
	if err != nil {
		t.Fatalf("parse manifest url: %v", err)
	}
	fetcher := testFetcher(true)
	manifest, hash, err := fetcher.FetchManifest(context.Background(), "conn-1", manifestURL, nil, 50)
	if err != nil {
		t.Fatalf("fetch manifest: %v", err)
	}
	if len(manifest.Items) != 2 || hash == "" {
		t.Fatalf("unexpected manifest: %+v hash=%q", manifest, hash)
	}
	if manifest.Items[0].URL != server.URL+"/files/a.md" {
		t.Fatalf("relative item was not resolved: %q", manifest.Items[0].URL)
	}
	if ItemFilename(manifest.Items[0]) != "a.md" {
		t.Fatalf("path hint should win: %q", ItemFilename(manifest.Items[0]))
	}

	reordered := Manifest{Items: []ManifestItem{manifest.Items[1], manifest.Items[0]}}
	if got := manifestHash(reordered); got != hash {
		t.Fatalf("manifest hash must not depend on item order: %q != %q", got, hash)
	}
	changed := manifest
	changed.Items = append([]ManifestItem(nil), manifest.Items...)
	changed.Items[0].Version = "2"
	if manifestHash(changed) == hash {
		t.Fatalf("manifest hash must change with a remote version")
	}
}

func TestFetchManifestAcceptsEmptySnapshot(t *testing.T) {
	_, manifestURL := manifestServer(t, []map[string]any{})
	manifest, hash, err := testFetcher(true).FetchManifest(context.Background(), "conn-1", manifestURL, nil, 50)
	if err != nil {
		t.Fatalf("empty manifest must be a valid snapshot: %v", err)
	}
	if len(manifest.Items) != 0 || hash == "" {
		t.Fatalf("unexpected empty manifest: %+v hash=%q", manifest, hash)
	}
	if hash != manifestHash(Manifest{Items: []ManifestItem{}}) {
		t.Fatalf("empty manifest hash must be stable")
	}
	if hash == manifestHash(Manifest{Items: []ManifestItem{{ExternalID: "a"}}}) {
		t.Fatalf("empty manifest hash must differ from a populated snapshot")
	}
}

func TestFetchManifestRejectsDuplicateAndMissingIDs(t *testing.T) {
	cases := [][]map[string]any{
		{{"id": "a", "url": "a.md"}, {"id": "a", "url": "b.md"}},
		{{"url": "a.md"}},
		{{"id": "a", "url": "ftp://example.com/a.md"}},
		{{"id": "a", "url": "a.md", "sha256": "not-a-digest"}},
	}
	for _, items := range cases {
		_, manifestURL := manifestServer(t, items)
		if _, _, err := testFetcher(true).FetchManifest(context.Background(), "conn-1", manifestURL, nil, 50); err == nil {
			t.Fatalf("expected manifest validation error for %v", items)
		}
	}
}

func TestSSRFGuardBlocksPrivateNetworksUnlessExplicitlyAllowed(t *testing.T) {
	server, manifestURL := manifestServer(t, []map[string]any{{"id": "a", "url": "a.md"}})
	if _, _, err := testFetcher(false).FetchManifest(context.Background(), "conn-1", manifestURL, nil, 50); err == nil || !strings.Contains(err.Error(), "private or reserved") {
		t.Fatalf("expected private-address rejection, got %v", err)
	}
	if _, _, err := testFetcher(true).FetchManifest(context.Background(), "conn-1", manifestURL, nil, 50); err != nil {
		t.Fatalf("private addresses should be allowed when configured: %v", err)
	}
	_ = server
}

func TestFetchRejectsCrossHostRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"a","url":"a.md"}]}`))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/manifest.json", http.StatusFound)
	}))
	defer redirector.Close()
	parsed, _ := ParseManifestURL(redirector.URL + "/manifest.json")
	if _, _, err := testFetcher(true).FetchManifest(context.Background(), "conn-1", parsed, nil, 50); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("cross-host redirect must be rejected, got %v", err)
	}
}

func TestCredentialIsSentOnlyToManifestHost(t *testing.T) {
	var sameHostAuth, crossHostAuth atomic.Value
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.json" {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
				{"id": "same", "url": "/same.md"},
				{"id": "cross", "url": crossFileURL},
			}})
			return
		}
		sameHostAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("# same"))
	}))
	defer fileServer.Close()
	crossFileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		crossHostAuth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("# cross"))
	}))
	defer crossFileServer.Close()
	crossFileURL = crossFileServer.URL + "/cross.md"

	manifestURL, _ := ParseManifestURL(fileServer.URL + "/manifest.json")
	fetcher := testFetcher(true)
	credential := &Credentials{Type: "bearer", Token: "secret-token"}
	manifest, _, err := fetcher.FetchManifest(context.Background(), "conn-1", manifestURL, credential, 50)
	if err != nil {
		t.Fatalf("fetch manifest: %v", err)
	}
	if _, err := fetcher.FetchFile(context.Background(), "conn-1", manifest.Items[0], manifestURL, credential, 50); err != nil {
		t.Fatalf("fetch same-host file: %v", err)
	}
	if _, err := fetcher.FetchFile(context.Background(), "conn-1", manifest.Items[1], manifestURL, credential, 50); err != nil {
		t.Fatalf("fetch cross-host file: %v", err)
	}
	if got, _ := sameHostAuth.Load().(string); got != "Bearer secret-token" {
		t.Fatalf("same-host request must carry the credential, got %q", got)
	}
	if got, _ := crossHostAuth.Load().(string); got != "" {
		t.Fatalf("cross-host request must not carry the credential, got %q", got)
	}
}

var crossFileURL string

func TestFetchRetriesTransientStatus(t *testing.T) {
	var attempts atomic.Int32
	file := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.json" {
			if attempts.Add(1) < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"a","url":"a.md"}]}`))
			return
		}
		_, _ = w.Write([]byte("# a"))
	}))
	defer file.Close()
	manifestURL, _ := ParseManifestURL(file.URL + "/manifest.json")
	manifest, _, err := testFetcher(true).FetchManifest(context.Background(), "conn-1", manifestURL, nil, 50)
	if err != nil {
		t.Fatalf("expected retry to succeed: %v", err)
	}
	if attempts.Load() != 3 || len(manifest.Items) != 1 {
		t.Fatalf("unexpected retry result: attempts=%d items=%d", attempts.Load(), len(manifest.Items))
	}
}

func TestFetchItemChecksumMismatchIsTerminal(t *testing.T) {
	file := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("# a"))
	}))
	defer file.Close()
	manifestURL, _ := ParseManifestURL(file.URL + "/manifest.json")
	item := ManifestItem{ExternalID: "a", URL: file.URL + "/a.md", SHA256: strings.Repeat("b", 64)}
	_, err := testFetcher(true).FetchFile(context.Background(), "conn-1", item, manifestURL, nil, 50)
	if err == nil || !strings.Contains(err.Error(), "checksum") || IsRetryable(err) {
		t.Fatalf("expected terminal checksum error, got %v", err)
	}
}

func TestItemFilenameStripsTraversal(t *testing.T) {
	if got := ItemFilename(ManifestItem{ExternalID: "a", URL: "https://example.com/files/x.pdf", Path: "../../etc/passwd.md"}); got != "passwd.md" {
		t.Fatalf("unexpected basename: %q", got)
	}
	if got := ItemFilename(ManifestItem{ExternalID: "a", URL: "https://example.com/x%20y.txt"}); got != "x y.txt" {
		t.Fatalf("unexpected url basename: %q", got)
	}
}

func TestSameOriginRequiresSchemeAndHost(t *testing.T) {
	cases := []struct {
		origin string
		target string
		want   bool
	}{
		{"https://example.com/m.json", "https://example.com/a.md", true},
		{"https://EXAMPLE.com/m.json", "https://example.com/a.md", true},
		{"https://example.com/m.json", "http://example.com/a.md", false},
		{"http://example.com/m.json", "https://example.com/a.md", false},
		{"https://example.com/m.json", "https://cdn.example.com/a.md", false},
		{"https://example.com:8443/m.json", "https://example.com:9443/a.md", false},
	}
	for _, test := range cases {
		origin, _ := url.Parse(test.origin)
		target, _ := url.Parse(test.target)
		if got := sameOrigin(origin, target); got != test.want {
			t.Fatalf("sameOrigin(%q, %q) = %v, want %v", test.origin, test.target, got, test.want)
		}
	}
	if sameOrigin(nil, nil) {
		t.Fatalf("nil origins are never the same origin")
	}
}

func TestRateLimiterSpacesRequestsPerConnector(t *testing.T) {
	fetcher := testFetcher(true)
	first := fetcher.limiter("conn-a", 0)
	second := fetcher.limiter("conn-a", 0)
	if first != second {
		t.Fatalf("expected one limiter per connector")
	}
	if other := fetcher.limiter("conn-b", 10); other == first {
		t.Fatalf("expected distinct limiters for distinct connectors")
	}
	if got, want := first.interval, time.Duration(float64(time.Second)/50); got != want {
		t.Fatalf("default rate interval = %s, want %s", got, want)
	}

	faster := fetcher.limiter("conn-a", 20)
	if faster == first {
		t.Fatalf("a changed rate must not reuse the cached limiter")
	}
	if got, want := faster.interval, 50*time.Millisecond; got != want {
		t.Fatalf("updated rate interval = %s, want %s", got, want)
	}
	if again := fetcher.limiter("conn-a", 20); again != faster {
		t.Fatalf("the same rate must reuse the cached limiter")
	}

	limiter := &rateLimiter{interval: 60 * time.Millisecond}
	started := time.Now()
	if err := limiter.wait(context.Background()); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 20*time.Millisecond {
		t.Fatalf("first wait should be immediate, took %s", elapsed)
	}
	if err := limiter.wait(context.Background()); err != nil {
		t.Fatalf("second wait: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("second wait should space requests, took %s", elapsed)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	waited := limiter.wait(ctx)
	if waited == nil {
		t.Fatalf("expected cancellation to interrupt the limiter")
	}
}

func TestBlockedIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "::1", "0.0.0.0"}
	for _, raw := range blocked {
		if !blockedIP(net.ParseIP(raw)) {
			t.Fatalf("expected %s to be blocked", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888"} {
		if blockedIP(net.ParseIP(raw)) {
			t.Fatalf("expected %s to be allowed", raw)
		}
	}
}
