package knowledgebase

import (
	"strings"
	"testing"
)

func TestNewID(t *testing.T) {
	id, err := newID("kb")
	if err != nil {
		t.Fatalf("newID returned error: %v", err)
	}
	if !strings.HasPrefix(id, "kb-") || len(id) != len("kb-")+24 {
		t.Fatalf("unexpected knowledge base id %q", id)
	}
}
