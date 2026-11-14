package inboxlease

import (
	"context"
	"testing"
	"time"
)

func TestOwnerAndLeaseHelpers(t *testing.T) {
	var owner Owner
	first := owner.Get()
	if first == "" || owner.Get() != first {
		t.Fatal("Owner.Get must return one stable identity")
	}
	if NewOwner() == "" {
		t.Fatal("NewOwner must return a non-empty identity")
	}

	if lease := New("worker", 0); lease.ttl() != DefaultTTL {
		t.Fatalf("zero TTL = %v, want default", lease.ttl())
	}
	if lease := New("worker", 3*time.Second); lease.HeartbeatInterval() != time.Second {
		t.Fatalf("heartbeat = %v, want 1s", lease.HeartbeatInterval())
	}

	db := newLeaseDB(t)
	ctx := context.Background()
	if _, err := Claim(ctx, db, "key", "task", 0, New("", time.Minute)); err == nil {
		t.Fatal("Claim without an owner must fail")
	}
	if err := Release(ctx, db, "missing", ""); err != nil {
		t.Fatalf("Release without an owner = %v", err)
	}
}
