package presets

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openPresetStore(t *testing.T) (*Resolver, *Catalog) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "presets.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.TenantPreset{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	builtin, err := LoadBuiltin()
	if err != nil {
		t.Fatalf("load builtin: %v", err)
	}
	return NewResolver(builtin, NewStore(db)), builtin
}

func TestTenantPresetCloneEditAndBuiltinImmutability(t *testing.T) {
	resolver, builtin := openPresetStore(t)
	ctx := context.Background()
	base, err := builtin.Get("general-enterprise", "1.0.0")
	if err != nil {
		t.Fatalf("builtin: %v", err)
	}
	base.Name = "team-pack"
	base.Version = "1.0.0"
	if _, err := resolver.Create(ctx, "tenant-1", "owner", base); err != nil {
		t.Fatalf("create custom: %v", err)
	}
	if _, err := resolver.Create(ctx, "tenant-1", "owner", base); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate must fail, got %v", err)
	}
	base.Retrieval["top_k"] = []byte("10")
	if _, err := resolver.Update(ctx, "tenant-1", "team-pack", "1.0.0", base); err != nil {
		t.Fatalf("update custom: %v", err)
	}
	loaded, err := resolver.Get(ctx, "tenant-1", "team-pack", "1.0.0")
	if err != nil || string(loaded.Retrieval["top_k"]) != "10" {
		t.Fatalf("updated pack = %+v, %v", loaded, err)
	}

	builtinManifest, _ := builtin.Get("general-enterprise", "1.0.0")
	if _, err := resolver.Update(ctx, "tenant-1", "general-enterprise", "1.0.0", builtinManifest); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("builtin update must fail, got %v", err)
	}
	if err := resolver.Delete(ctx, "tenant-1", "general-enterprise", "1.0.0"); err == nil || !strings.Contains(err.Error(), "cannot be deleted") {
		t.Fatalf("builtin delete must fail, got %v", err)
	}
	if _, err := resolver.Clone(ctx, "tenant-1", "owner", "general-enterprise", "1.0.0", "cloned-pack", "2.0.0"); err != nil {
		t.Fatalf("clone: %v", err)
	}
	entries, err := resolver.List(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	sources := map[string]int{}
	for _, entry := range entries {
		sources[entry.Source]++
	}
	if sources[SourceBuiltin] == 0 || sources[SourceTenant] < 2 {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if source := resolver.Source(ctx, "tenant-1", "cloned-pack", "2.0.0"); source != SourceTenant {
		t.Fatalf("clone source = %q", source)
	}
}
