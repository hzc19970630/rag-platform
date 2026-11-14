package presets

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/apperror"
)

func TestTenantPresetsAreScopedPerTenant(t *testing.T) {
	resolver, builtin := openPresetStore(t)
	ctx := context.Background()
	manifest, err := builtin.Get("general-enterprise", "1.0.0")
	if err != nil {
		t.Fatalf("builtin: %v", err)
	}
	manifest.Name = "scoped-pack"
	if _, err := resolver.Create(ctx, "tenant-1", "owner", manifest); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := resolver.Get(ctx, "tenant-1", "scoped-pack", "1.0.0"); err != nil {
		t.Fatalf("owner must read its pack: %v", err)
	}
	if _, err := resolver.Get(ctx, "tenant-2", "scoped-pack", "1.0.0"); err == nil || !strings.Contains(err.Error(), "unknown preset") {
		t.Fatalf("cross-tenant read must fail, got %v", err)
	}
	if source := resolver.Source(ctx, "tenant-2", "scoped-pack", "1.0.0"); source != "" {
		t.Fatalf("cross-tenant source = %q, want empty", source)
	}
	if err := resolver.Delete(ctx, "tenant-2", "scoped-pack", "1.0.0"); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("cross-tenant delete = %v, want not found", err)
	}
	entries, err := resolver.List(ctx, "tenant-2")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, entry := range entries {
		if entry.Source == SourceTenant {
			t.Fatalf("tenant-2 must not see tenant-1 packs: %+v", entries)
		}
	}
}

func TestUpdateKeepsNameAndVersionImmutable(t *testing.T) {
	resolver, builtin := openPresetStore(t)
	ctx := context.Background()
	manifest, err := builtin.Get("general-enterprise", "1.0.0")
	if err != nil {
		t.Fatalf("builtin: %v", err)
	}
	manifest.Name = "locked-pack"
	if _, err := resolver.Create(ctx, "tenant-1", "owner", manifest); err != nil {
		t.Fatalf("create: %v", err)
	}

	renamed := manifest
	renamed.Name = "renamed-pack"
	if _, err := resolver.Update(ctx, "tenant-1", "locked-pack", "1.0.0", renamed); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("rename must fail, got %v", err)
	}
	reversioned := manifest
	reversioned.Version = "2.0.0"
	if _, err := resolver.Update(ctx, "tenant-1", "locked-pack", "1.0.0", reversioned); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("version change must fail, got %v", err)
	}
	missing := manifest
	missing.Name = "missing-pack"
	if _, err := resolver.Update(ctx, "tenant-1", "missing-pack", "1.0.0", missing); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("missing update = %v, want not found", err)
	}
}

func TestCloneRejectsReservedAndDuplicateTargets(t *testing.T) {
	resolver, _ := openPresetStore(t)
	ctx := context.Background()
	if _, err := resolver.Clone(ctx, "tenant-1", "owner", "general-enterprise", "1.0.0", "general-enterprise", "1.0.0"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("cloning onto a built-in name must fail, got %v", err)
	}
	if _, err := resolver.Clone(ctx, "tenant-1", "owner", "general-enterprise", "1.0.0", "copy-pack", "1.0.0"); err != nil {
		t.Fatalf("first clone: %v", err)
	}
	if _, err := resolver.Clone(ctx, "tenant-1", "owner", "general-enterprise", "1.0.0", "copy-pack", "1.0.0"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate clone must fail, got %v", err)
	}
	if _, err := resolver.Clone(ctx, "tenant-1", "owner", "missing-pack", "1.0.0", "copy-pack-2", "1.0.0"); err == nil {
		t.Fatal("cloning an unknown source must fail")
	}
}
