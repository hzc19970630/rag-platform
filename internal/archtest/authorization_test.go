package archtest

import "testing"

func TestRepositoryAuthorizationUsesResourceSeam(t *testing.T) {
	files, err := LoadTenancyFiles("../..", "internal", "cmd")
	if err != nil {
		t.Fatalf("load repository files: %v", err)
	}
	for _, violation := range CheckAuthorization(files) {
		t.Error(violation)
	}
}

func TestCheckAuthorizationDetectsBypasses(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   int
	}{
		{
			name:   "legacy knowledge-base helper",
			source: "s.authz.AuthorizeKnowledgeBase(ctx, principal, id, action)",
			want:   1,
		},
		{
			name:   "generic object check in a domain package",
			source: "s.authz.Authorize(ctx, principal, action, object)",
			want:   1,
		},
		{
			name:   "hand-built policy path",
			source: `path := resourcePath(id)`,
			want:   1,
		},
		{
			name:   "resource seam",
			source: "s.authz.AuthorizeResource(ctx, principal, authz.ResourceTarget{KnowledgeBaseID: id}, action)",
		},
		{
			name:   "collection check stays allowed",
			source: "s.authz.AuthorizeKnowledgeBases(ctx, principal, \"write\")",
		},
		{
			name:   "scope and filter helpers stay allowed",
			source: "s.authz.FilterReadable(ctx, scope, refs)",
		},
		{
			name:   "comments are ignored",
			source: "// use AuthorizeResource instead of .Authorize( here",
		},
	}
	for _, testCase := range cases {
		files := []TenancyFile{{Path: "internal/sample/service.go", Source: []byte(testCase.source)}}
		if got := len(CheckAuthorization(files)); got != testCase.want {
			t.Fatalf("%s: got %d violations, want %d", testCase.name, got, testCase.want)
		}
	}
}
