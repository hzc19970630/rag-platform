package datasource

import (
	"context"
	"net/url"
	"testing"
)

type fakeConnector struct {
	manifestCalls int
	fileCalls     int
}

func (f *fakeConnector) Type() string { return "fake_source" }

func (f *fakeConnector) NormalizeConfig(input ConnectorConfig, _ int) (ConnectorConfig, error) {
	return input, nil
}

func (f *fakeConnector) FetchManifest(_ context.Context, _ *Fetcher, connectorID string, _ *url.URL, _ *Credentials, _ float64) (Manifest, string, error) {
	f.manifestCalls++
	return Manifest{Items: []ManifestItem{{ExternalID: connectorID}}}, "hash", nil
}

func (f *fakeConnector) FetchFile(_ context.Context, _ *Fetcher, _ string, item ManifestItem, _ *url.URL, _ *Credentials, _ float64) (RemoteFile, error) {
	f.fileCalls++
	return RemoteFile{ContentHash: item.ExternalID}, nil
}

func TestConnectorFactoryContract(t *testing.T) {
	fake := &fakeConnector{}
	if err := RegisterConnectorFactory(func() Connector { return fake }); err != nil {
		t.Fatalf("register fake connector: %v", err)
	}
	port, ok := ConnectorFor(" FAKE_SOURCE ")
	if !ok {
		t.Fatalf("registered connector type must resolve case-insensitively")
	}
	if port.Type() != "fake_source" {
		t.Fatalf("unexpected connector type %q", port.Type())
	}
	manifestURL, err := ParseManifestURL("https://example.com/manifest.json")
	if err != nil {
		t.Fatalf("parse manifest url: %v", err)
	}
	manifest, hash, err := port.FetchManifest(context.Background(), nil, "conn-1", manifestURL, nil, 0)
	if err != nil || hash != "hash" || len(manifest.Items) != 1 || manifest.Items[0].ExternalID != "conn-1" {
		t.Fatalf("manifest delegation failed: %+v %q %v", manifest, hash, err)
	}
	if _, err := port.FetchFile(context.Background(), nil, "conn-1", ManifestItem{ExternalID: "item-1"}, manifestURL, nil, 0); err != nil {
		t.Fatalf("file delegation failed: %v", err)
	}
	if fake.manifestCalls != 1 || fake.fileCalls != 1 {
		t.Fatalf("fake connector was not called: %d %d", fake.manifestCalls, fake.fileCalls)
	}
	if _, ok := ConnectorFor("missing_source"); ok {
		t.Fatalf("unknown connector type must not resolve")
	}
}

func TestHTTPManifestConnectorIsRegistered(t *testing.T) {
	port, ok := ConnectorFor(TypeHTTPManifest)
	if !ok {
		t.Fatalf("http_manifest connector must be registered")
	}
	if _, ok := port.(HTTPManifestConnector); !ok {
		t.Fatalf("unexpected connector implementation %T", port)
	}
}
