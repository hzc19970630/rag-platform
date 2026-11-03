package authz

import (
	"reflect"
	"testing"
)

func TestCapabilitiesForRole(t *testing.T) {
	tests := []struct {
		role string
		want []string
	}{
		{role: "owner", want: []string{"knowledge_base:read", "knowledge_base:write", "knowledge_base:delete", "knowledge_base:manage_members"}},
		{role: "admin", want: []string{"knowledge_base:read", "knowledge_base:write", "knowledge_base:delete", "knowledge_base:manage_members"}},
		{role: "editor", want: []string{"knowledge_base:read", "knowledge_base:write"}},
		{role: "viewer", want: []string{"knowledge_base:read"}},
	}
	for _, test := range tests {
		if got := capabilitiesForRole(test.role); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("role %q capabilities = %#v, want %#v", test.role, got, test.want)
		}
	}
}

func TestContains(t *testing.T) {
	if !contains([]string{"kb-a", "kb-b"}, "kb-b") {
		t.Fatal("expected value to be found")
	}
	if contains([]string{"kb-a"}, "kb-b") {
		t.Fatal("did not expect value to be found")
	}
}
