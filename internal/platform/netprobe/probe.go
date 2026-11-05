// Package netprobe is the shared liveness check used by remote model adapters.
// It answers one question: can this process reach the provider with the
// configured credential? It deliberately does not assert a specific status
// body: providers expose different health paths, and a reachable 404 is still
// evidence that the gateway is up.
package netprobe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const probeTimeout = 3 * time.Second

var client = &http.Client{Timeout: probeTimeout}

// Check reports whether endpoint answered. 401/403 is a failure because the
// configured credential was rejected; 5xx is a failure because the provider
// itself reports it cannot serve; any other status below 500 counts as
// reachable.
func Check(ctx context.Context, endpoint, apiKey string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("probe %s: %w", endpoint, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return fmt.Errorf("probe %s: provider rejected the configured credential with %d", endpoint, response.StatusCode)
	case response.StatusCode >= http.StatusInternalServerError:
		return fmt.Errorf("probe %s: provider returned %d", endpoint, response.StatusCode)
	default:
		return nil
	}
}
