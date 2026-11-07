package datasource

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/hzc19970630/rag-platform/internal/apperror"
)

// Connector is the source-specific half of a sync run. The worker owns run,
// cursor and item bookkeeping; the connector owns configuration validation and
// how a manifest and its files are fetched. New source types register a
// factory instead of changing the worker.
type Connector interface {
	Type() string
	NormalizeConfig(input ConnectorConfig, maxItemsPerRun int) (ConnectorConfig, error)
	FetchManifest(ctx context.Context, fetcher *Fetcher, connectorID string, manifestURL *url.URL, credential *Credentials, requestsPerSecond float64) (Manifest, string, error)
	FetchFile(ctx context.Context, fetcher *Fetcher, connectorID string, item ManifestItem, manifestURL *url.URL, credential *Credentials, requestsPerSecond float64) (RemoteFile, error)
}

type ConnectorFactory func() Connector

var connectorRegistry = struct {
	sync.RWMutex
	factories map[string]ConnectorFactory
}{factories: map[string]ConnectorFactory{}}

// RegisterConnectorFactory adds a connector type. Registration is idempotent
// for the same type so both init wiring and tests can call it safely.
func RegisterConnectorFactory(factory ConnectorFactory) error {
	if factory == nil {
		return fmt.Errorf("connector factory is required")
	}
	connector := factory()
	if connector == nil {
		return fmt.Errorf("connector factory returned nil")
	}
	connectorType := strings.ToLower(strings.TrimSpace(connector.Type()))
	if connectorType == "" {
		return fmt.Errorf("connector type is required")
	}
	connectorRegistry.Lock()
	defer connectorRegistry.Unlock()
	connectorRegistry.factories[connectorType] = factory
	return nil
}

// ConnectorFor resolves a registered connector type. The lookup is
// case-insensitive so stored values and API input agree.
func ConnectorFor(connectorType string) (Connector, bool) {
	connectorRegistry.RLock()
	factory, ok := connectorRegistry.factories[strings.ToLower(strings.TrimSpace(connectorType))]
	connectorRegistry.RUnlock()
	if !ok {
		return nil, false
	}
	connector := factory()
	if connector == nil {
		return nil, false
	}
	return connector, true
}

func ConnectorTypes() []string {
	connectorRegistry.RLock()
	defer connectorRegistry.RUnlock()
	types := make([]string, 0, len(connectorRegistry.factories))
	for connectorType := range connectorRegistry.factories {
		types = append(types, connectorType)
	}
	return types
}

// HTTPManifestConnector fetches an allow-listed HTTP JSON manifest and its
// linked files. It is the only source type the first phase ships.
type HTTPManifestConnector struct{}

func (HTTPManifestConnector) Type() string { return TypeHTTPManifest }

func (HTTPManifestConnector) NormalizeConfig(input ConnectorConfig, maxItemsPerRun int) (ConnectorConfig, error) {
	if _, err := ParseManifestURL(input.ManifestURL); err != nil {
		return ConnectorConfig{}, err
	}
	if input.RequestsPerSecond < 0 || input.RequestsPerSecond > 50 {
		return ConnectorConfig{}, apperror.Invalidf("requests per second must be between 0 and 50")
	}
	maxItems := input.MaxItemsPerRun
	if maxItems == 0 {
		maxItems = min(maxItemsPerRun, defaultMaxItemsPerRun)
	}
	if maxItems < 1 || maxItems > maxItemsPerRun {
		return ConnectorConfig{}, apperror.Invalidf("max items per run must be between 1 and %d", maxItemsPerRun)
	}
	aclMode := strings.TrimSpace(input.ACLMode)
	if aclMode == "" {
		aclMode = ACLModeKnowledgeBase
	}
	if aclMode != ACLModeKnowledgeBase {
		return ConnectorConfig{}, apperror.Invalidf("unsupported ACL mode")
	}
	return ConnectorConfig{
		ManifestURL: strings.TrimSpace(input.ManifestURL), RequestsPerSecond: input.RequestsPerSecond,
		MaxItemsPerRun: maxItems, ACLMode: aclMode,
	}, nil
}

func (HTTPManifestConnector) FetchManifest(ctx context.Context, fetcher *Fetcher, connectorID string, manifestURL *url.URL, credential *Credentials, requestsPerSecond float64) (Manifest, string, error) {
	return fetcher.FetchManifest(ctx, connectorID, manifestURL, credential, requestsPerSecond)
}

func (HTTPManifestConnector) FetchFile(ctx context.Context, fetcher *Fetcher, connectorID string, item ManifestItem, manifestURL *url.URL, credential *Credentials, requestsPerSecond float64) (RemoteFile, error) {
	return fetcher.FetchFile(ctx, connectorID, item, manifestURL, credential, requestsPerSecond)
}

func init() {
	_ = RegisterConnectorFactory(func() Connector { return HTTPManifestConnector{} })
}
