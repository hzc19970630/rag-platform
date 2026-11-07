// Package tenantquota resolves tenant limits from deployment defaults and the
// per-tenant override stored in tenants.quota. A zero value means unlimited at
// both tiers; an override of zero explicitly removes the deployment limit.
package tenantquota

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

// Limits is the effective ceiling for one tenant. Zero means unlimited.
type Limits struct {
	MaxDocuments       int64
	MaxStorageBytes    int64
	ChatRequestsPerDay int64
}

// Overrides is the tenant-level part of the quota JSON document. A nil pointer
// inherits the deployment default; a pointer to zero is an explicit unlimited.
type Overrides struct {
	MaxDocuments       *int64 `json:"max_documents,omitempty"`
	MaxStorageBytes    *int64 `json:"max_storage_bytes,omitempty"`
	ChatRequestsPerDay *int64 `json:"chat_requests_per_day,omitempty"`
}

// Field keeps the resolved value and where it came from, so the admin surface
// can distinguish an inherited limit from an explicit one.
type Field struct {
	Value      int64
	Default    int64
	Overridden bool
}

// Effective is the resolved quota for one tenant.
type Effective struct {
	MaxDocuments       Field
	MaxStorageBytes    Field
	ChatRequestsPerDay Field
}

// Limits returns the effective values in the form enforcement consumes.
func (e Effective) Limits() Limits {
	return Limits{
		MaxDocuments:       e.MaxDocuments.Value,
		MaxStorageBytes:    e.MaxStorageBytes.Value,
		ChatRequestsPerDay: e.ChatRequestsPerDay.Value,
	}
}

// Resolve applies overrides on top of the deployment defaults.
func Resolve(defaults Limits, overrides Overrides) Effective {
	return Effective{
		MaxDocuments:       resolveField(defaults.MaxDocuments, overrides.MaxDocuments),
		MaxStorageBytes:    resolveField(defaults.MaxStorageBytes, overrides.MaxStorageBytes),
		ChatRequestsPerDay: resolveField(defaults.ChatRequestsPerDay, overrides.ChatRequestsPerDay),
	}
}

func resolveField(defaultValue int64, override *int64) Field {
	if override == nil {
		return Field{Value: defaultValue, Default: defaultValue}
	}
	return Field{Value: *override, Default: defaultValue, Overridden: true}
}

// Validate rejects negative limits. Zero is a documented value (unlimited), so
// an empty override map stays valid.
func (o Overrides) Validate() error {
	fields := []struct {
		name  string
		value *int64
	}{
		{"maxDocuments", o.MaxDocuments},
		{"maxStorageBytes", o.MaxStorageBytes},
		{"chatRequestsPerDay", o.ChatRequestsPerDay},
	}
	for _, field := range fields {
		if field.value != nil && *field.value < 0 {
			return fmt.Errorf("%s must not be negative", field.name)
		}
	}
	return nil
}

// Encode serializes overrides for the JSONB column.
func Encode(overrides Overrides) (string, error) {
	if err := overrides.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(overrides)
	if err != nil {
		return "", fmt.Errorf("encode tenant quota: %w", err)
	}
	return string(encoded), nil
}

// Parse decodes the stored JSONB value. Empty and "{}" both mean no overrides.
func Parse(raw string) (Overrides, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return Overrides{}, nil
	}
	var overrides Overrides
	if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
		return Overrides{}, fmt.Errorf("parse tenant quota: %w", err)
	}
	if err := overrides.Validate(); err != nil {
		return Overrides{}, fmt.Errorf("parse tenant quota: %w", err)
	}
	return overrides, nil
}

// Resolver loads overrides and resolves them against the deployment defaults.
// Reads join the request transaction when one is attached, so a quota change
// made earlier in the same request is visible to the enforcement check.
type Resolver struct {
	db       *gorm.DB
	defaults Limits
}

func NewResolver(db *gorm.DB, defaults Limits) *Resolver {
	return &Resolver{db: db, defaults: defaults}
}

func (r *Resolver) Defaults() Limits {
	return r.defaults
}

func (r *Resolver) Overrides(ctx context.Context, tenantID string) (Overrides, error) {
	var tenant models.Tenant
	if err := tenantdb.FromContext(ctx, r.db).Select("id", "quota").First(&tenant, "id = ?", tenantID).Error; err != nil {
		return Overrides{}, fmt.Errorf("load tenant quota: %w", err)
	}
	return Parse(tenant.Quota)
}

func (r *Resolver) Effective(ctx context.Context, tenantID string) (Effective, error) {
	overrides, err := r.Overrides(ctx, tenantID)
	if err != nil {
		return Effective{}, err
	}
	return Resolve(r.defaults, overrides), nil
}

func (r *Resolver) Limits(ctx context.Context, tenantID string) (Limits, error) {
	effective, err := r.Effective(ctx, tenantID)
	if err != nil {
		return Limits{}, err
	}
	return effective.Limits(), nil
}

// Save replaces the tenant's override object. The caller is responsible for
// authorization and auditing.
func (r *Resolver) Save(ctx context.Context, tenantID string, overrides Overrides) error {
	return r.SaveTo(ctx, r.db, tenantID, overrides)
}

// SaveTo runs the update on the caller's handle, which lets a service keep the
// quota change and its audit row in one transaction.
func (r *Resolver) SaveTo(ctx context.Context, db *gorm.DB, tenantID string, overrides Overrides) error {
	encoded, err := Encode(overrides)
	if err != nil {
		return err
	}
	result := db.WithContext(ctx).Model(&models.Tenant{}).Where("id = ?", tenantID).Update("quota", encoded)
	if result.Error != nil {
		return fmt.Errorf("save tenant quota: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("save tenant quota: %w", gorm.ErrRecordNotFound)
	}
	return nil
}
