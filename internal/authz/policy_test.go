package authz

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestKnownCapabilityClosedSet(t *testing.T) {
	valid := []string{
		"knowledge_base:read",
		"knowledge_base:write",
		"knowledge_base:delete",
		"knowledge_base:manage_members",
		"chat",
	}
	for _, capability := range valid {
		if !KnownCapability(capability) {
			t.Fatalf("KnownCapability(%q) = false, want true", capability)
		}
	}
	for _, capability := range []string{"", "tenant:admin", "knowledge_base:admin", "CHAT"} {
		if KnownCapability(capability) {
			t.Fatalf("KnownCapability(%q) = true, want false", capability)
		}
	}
}

func TestIntersect(t *testing.T) {
	tests := []struct {
		name  string
		left  []string
		right []string
		want  []string
	}{
		{name: "nil left", left: nil, right: []string{"a"}, want: []string{}},
		{name: "empty right", left: []string{"a"}, right: nil, want: []string{}},
		{name: "keeps left order and duplicates", left: []string{"b", "a", "b"}, right: []string{"a", "b"}, want: []string{"b", "a", "b"}},
		{name: "drops missing values", left: []string{"a", "b"}, right: []string{"b"}, want: []string{"b"}},
		{name: "empty left", left: []string{}, right: []string{"a"}, want: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := intersect(test.left, test.right)
			if len(got) != len(test.want) {
				t.Fatalf("intersect = %#v, want %#v", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("intersect = %#v, want %#v", got, test.want)
				}
			}
		})
	}
}

func TestStartInvalidationSkipsEmptyAddress(t *testing.T) {
	service := &Service{}
	if err := service.StartInvalidation(context.Background(), "  ", ""); err != nil {
		t.Fatalf("StartInvalidation(empty) = %v, want nil", err)
	}
}

func TestStartInvalidationReportsUnreachableRedis(t *testing.T) {
	service := &Service{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := service.StartInvalidation(ctx, "127.0.0.1:1", "")
	if err == nil {
		t.Fatal("StartInvalidation(unreachable) = nil, want error")
	}
	if !strings.Contains(err.Error(), "connect authorization invalidation redis") {
		t.Fatalf("StartInvalidation error = %v", err)
	}
	if closeErr := service.Close(); closeErr != nil {
		t.Fatalf("Close after failed invalidation = %v", closeErr)
	}
}

func TestCloseWithoutRedis(t *testing.T) {
	service := &Service{}
	if err := service.Close(); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
}

func TestRefreshPoliciesClearsCaches(t *testing.T) {
	db := openAuthzDB(t)
	seedAuthz(t, db)
	service, err := NewService(NewPostgresStores(db))
	if err != nil {
		t.Fatalf("new authz service: %v", err)
	}
	ctx := context.Background()
	if _, err := service.BuildScope(ctx, Principal{TenantID: "tenant-a", SubjectID: "owner-a"}, QueryTarget{}); err != nil {
		t.Fatalf("BuildScope: %v", err)
	}
	service.mu.RLock()
	tenants := len(service.tenants)
	scopes := len(service.scopes)
	revisions := len(service.revisionByTenant)
	service.mu.RUnlock()
	if tenants == 0 || scopes == 0 || revisions == 0 {
		t.Fatalf("caches not populated: tenants=%d scopes=%d revisions=%d", tenants, scopes, revisions)
	}
	if err := service.RefreshPolicies(ctx); err != nil {
		t.Fatalf("RefreshPolicies: %v", err)
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	if len(service.tenants) != 0 || len(service.scopes) != 0 || len(service.revisionByTenant) != 0 {
		t.Fatalf("caches not cleared: %d/%d/%d", len(service.tenants), len(service.scopes), len(service.revisionByTenant))
	}
}
