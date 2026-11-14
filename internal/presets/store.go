package presets

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

// SourceBuiltin and SourceTenant distinguish immutable deployment packs from
// tenant-owned copies.
const (
	SourceBuiltin = "builtin"
	SourceTenant  = "tenant"
)

// Entry is one catalog item with its origin.
type Entry struct {
	Source   string
	Manifest Manifest
}

// Store persists tenant custom presets.
type Store struct {
	db *gorm.DB
}

func NewStore(db *gorm.DB) *Store { return &Store{db: db} }

func (s *Store) list(ctx context.Context, tenantID string) ([]models.TenantPreset, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var rows []models.TenantPreset
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Where("tenant_id = ?", tenantID).
		Order("name, version DESC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list tenant presets: %w", err)
	}
	return rows, nil
}

func (s *Store) get(ctx context.Context, tenantID, name, version string) (Manifest, bool, error) {
	if s == nil || s.db == nil {
		return Manifest{}, false, nil
	}
	query := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Where("tenant_id = ? AND name = ?", tenantID, name)
	if version != "" {
		query = query.Where("version = ?", version)
	}
	var row models.TenantPreset
	if err := query.Order("version DESC").Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return Manifest{}, false, nil
		}
		return Manifest{}, false, fmt.Errorf("load tenant preset: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal([]byte(row.Manifest), &manifest); err != nil {
		return Manifest{}, false, fmt.Errorf("decode tenant preset: %w", err)
	}
	return manifest, true, nil
}

func (s *Store) create(ctx context.Context, tenantID, actor string, manifest Manifest) error {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode preset: %w", err)
	}
	id, err := newID()
	if err != nil {
		return err
	}
	now := time.Now()
	row := models.TenantPreset{
		ID: id, TenantID: tenantID, Name: manifest.Name, Version: manifest.Version,
		Manifest: string(raw), CreatedBy: actor, CreatedAt: now, UpdatedAt: now,
	}
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Create(&row).Error; err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "duplicate") || strings.Contains(message, "unique") {
			return fmt.Errorf("%w: preset %s@%s already exists", apperror.ErrConflict, manifest.Name, manifest.Version)
		}
		return fmt.Errorf("create tenant preset: %w", err)
	}
	return nil
}

func (s *Store) update(ctx context.Context, tenantID, name, version string, manifest Manifest) error {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode preset: %w", err)
	}
	result := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.TenantPreset{}).
		Where("tenant_id = ? AND name = ? AND version = ?", tenantID, name, version).
		Updates(map[string]any{"manifest": string(raw), "updated_at": time.Now()})
	if result.Error != nil {
		return fmt.Errorf("update tenant preset: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

func (s *Store) delete(ctx context.Context, tenantID, name, version string) error {
	result := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Where("tenant_id = ? AND name = ? AND version = ?", tenantID, name, version).
		Delete(&models.TenantPreset{})
	if result.Error != nil {
		return fmt.Errorf("delete tenant preset: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

// Resolver combines the immutable built-in catalog with tenant custom packs.
type Resolver struct {
	Builtin *Catalog
	Store   *Store
}

func NewResolver(builtin *Catalog, store *Store) *Resolver {
	return &Resolver{Builtin: builtin, Store: store}
}

// List returns tenant packs after built-ins, sorted by name/version.
func (r *Resolver) List(ctx context.Context, tenantID string) ([]Entry, error) {
	result := make([]Entry, 0)
	if r.Builtin != nil {
		for _, manifest := range r.Builtin.List() {
			result = append(result, Entry{Source: SourceBuiltin, Manifest: manifest})
		}
	}
	rows, err := r.Store.list(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		var manifest Manifest
		if err := json.Unmarshal([]byte(row.Manifest), &manifest); err != nil {
			continue
		}
		result = append(result, Entry{Source: SourceTenant, Manifest: manifest})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Manifest.Name != result[j].Manifest.Name {
			return result[i].Manifest.Name < result[j].Manifest.Name
		}
		return compareVersion(result[i].Manifest.Version, result[j].Manifest.Version) > 0
	})
	return result, nil
}

// Get resolves one pack. Tenant packs take precedence only for names that do
// not collide with a built-in name; built-in names are reserved.
func (r *Resolver) Get(ctx context.Context, tenantID, name, version string) (Manifest, error) {
	if r.Builtin != nil {
		if manifest, err := r.Builtin.Get(name, version); err == nil {
			return manifest, nil
		}
	}
	manifest, found, err := r.Store.get(ctx, tenantID, name, version)
	if err != nil {
		return Manifest{}, err
	}
	if !found {
		return Manifest{}, apperror.Invalidf("unknown preset %q version %q", name, version)
	}
	return manifest, nil
}

// Source reports whether a visible pack is built-in or tenant-owned.
func (r *Resolver) Source(ctx context.Context, tenantID, name, version string) string {
	if r.Builtin != nil {
		if _, err := r.Builtin.Get(name, version); err == nil {
			return SourceBuiltin
		}
	}
	if _, found, err := r.Store.get(ctx, tenantID, name, version); err == nil && found {
		return SourceTenant
	}
	return ""
}

// Create imports a manifest as a tenant pack. Built-in names are reserved.
func (r *Resolver) Create(ctx context.Context, tenantID, actor string, manifest Manifest) (Manifest, error) {
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	if r.Builtin != nil {
		if _, err := r.Builtin.Get(manifest.Name, manifest.Version); err == nil {
			return Manifest{}, apperror.Invalidf("preset name %q is reserved by the deployment", manifest.Name)
		}
	}
	if err := r.Store.create(ctx, tenantID, actor, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Update edits a tenant pack in place; version is immutable.
func (r *Resolver) Update(ctx context.Context, tenantID, name, version string, manifest Manifest) (Manifest, error) {
	if manifest.Name != name || manifest.Version != version {
		return Manifest{}, apperror.Invalidf("preset name and version are immutable")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	if r.Builtin != nil {
		if _, err := r.Builtin.Get(name, version); err == nil {
			return Manifest{}, apperror.Invalidf("built-in presets are immutable; clone it first")
		}
	}
	if err := r.Store.update(ctx, tenantID, name, version, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (r *Resolver) Delete(ctx context.Context, tenantID, name, version string) error {
	if r.Builtin != nil {
		if _, err := r.Builtin.Get(name, version); err == nil {
			return apperror.Invalidf("built-in presets cannot be deleted")
		}
	}
	return r.Store.delete(ctx, tenantID, name, version)
}

// Clone copies any visible pack into a tenant-owned name/version.
func (r *Resolver) Clone(ctx context.Context, tenantID, actor, sourceName, sourceVersion, targetName, targetVersion string) (Manifest, error) {
	source, err := r.Get(ctx, tenantID, sourceName, sourceVersion)
	if err != nil {
		return Manifest{}, err
	}
	source.Name = targetName
	source.Version = targetVersion
	return r.Create(ctx, tenantID, actor, source)
}

func newID() (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate preset id: %w", err)
	}
	return "preset-" + hex.EncodeToString(buffer), nil
}
