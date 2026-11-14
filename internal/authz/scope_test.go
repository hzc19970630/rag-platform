package authz

import (
	"context"
	"strings"
	"testing"
)

func TestCanReadEnforcesTenantScopeAndCapability(t *testing.T) {
	service := &Service{}
	readScope := AuthorizationScope{
		TenantID:         "tenant-a",
		KnowledgeBaseIDs: []string{"kb-a"},
		Capabilities:     []string{"knowledge_base:read"},
	}
	tests := []struct {
		name  string
		scope AuthorizationScope
		ref   ResourceRef
		want  bool
	}{
		{
			name:  "matching tenant and granted knowledge base",
			scope: readScope,
			ref:   ResourceRef{TenantID: "tenant-a", KnowledgeBaseID: "kb-a"},
			want:  true,
		},
		{
			name:  "cross tenant is denied",
			scope: readScope,
			ref:   ResourceRef{TenantID: "tenant-b", KnowledgeBaseID: "kb-a"},
			want:  false,
		},
		{
			name:  "knowledge base outside the scope is denied",
			scope: readScope,
			ref:   ResourceRef{TenantID: "tenant-a", KnowledgeBaseID: "kb-b"},
			want:  false,
		},
		{
			name: "capability without read is denied",
			scope: AuthorizationScope{
				TenantID:         "tenant-a",
				KnowledgeBaseIDs: []string{"kb-a"},
				Capabilities:     []string{"knowledge_base:write"},
			},
			ref:  ResourceRef{TenantID: "tenant-a", KnowledgeBaseID: "kb-a"},
			want: false,
		},
		{
			name: "empty capabilities allow read",
			scope: AuthorizationScope{
				TenantID:         "tenant-a",
				KnowledgeBaseIDs: []string{"kb-a"},
			},
			ref:  ResourceRef{TenantID: "tenant-a", KnowledgeBaseID: "kb-a"},
			want: true,
		},
		{
			name:  "nil knowledge base list is denied",
			scope: AuthorizationScope{TenantID: "tenant-a"},
			ref:   ResourceRef{TenantID: "tenant-a", KnowledgeBaseID: "kb-a"},
			want:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := service.CanRead(context.Background(), test.scope, test.ref)
			if err != nil {
				t.Fatalf("CanRead returned error: %v", err)
			}
			if got != test.want {
				t.Fatalf("CanRead = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFilterReadableKeepsOnlyAuthorizedRefsInOrder(t *testing.T) {
	service := &Service{}
	scope := AuthorizationScope{
		TenantID:         "tenant-a",
		KnowledgeBaseIDs: []string{"kb-a"},
	}
	refs := []ResourceRef{
		{TenantID: "tenant-a", KnowledgeBaseID: "kb-a", ResourceID: "r1"},
		{TenantID: "tenant-b", KnowledgeBaseID: "kb-b", ResourceID: "r2"},
		{TenantID: "tenant-a", KnowledgeBaseID: "kb-b", ResourceID: "r3"},
		{TenantID: "tenant-a", KnowledgeBaseID: "kb-a", ResourceID: "r4"},
	}
	got, err := service.FilterReadable(context.Background(), scope, refs)
	if err != nil {
		t.Fatalf("FilterReadable returned error: %v", err)
	}
	if len(got) != 2 || got[0].ResourceID != "r1" || got[1].ResourceID != "r4" {
		t.Fatalf("FilterReadable = %+v, want [r1 r4]", got)
	}

	empty, err := service.FilterReadable(context.Background(), scope, nil)
	if err != nil {
		t.Fatalf("FilterReadable(nil) returned error: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("FilterReadable(nil) = %+v, want empty", empty)
	}
}

func TestRedisScopeKeyIsStableAndNamespaced(t *testing.T) {
	key := scopeCacheKey(
		Principal{TenantID: "tenant-a", SubjectID: "owner-a"},
		QueryTarget{KnowledgeBaseID: "kb-a"},
		3,
	)
	first := redisScopeKey(key)
	second := redisScopeKey(key)
	if first != second {
		t.Fatalf("redisScopeKey is not deterministic: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "authz:scope:") {
		t.Fatalf("redisScopeKey = %q, want authz:scope: prefix", first)
	}
	if suffix := strings.TrimPrefix(first, "authz:scope:"); len(suffix) != 64 {
		t.Fatalf("redisScopeKey digest length = %d, want 64", len(suffix))
	}
	other := redisScopeKey(key + ":other")
	if other == first {
		t.Fatal("different scope keys must hash differently")
	}
	if strings.Contains(first, "tenant-a") {
		t.Fatal("redis scope key must not leak the raw tenant id")
	}
}
