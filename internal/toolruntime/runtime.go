// Package toolruntime resolves the KB-level tool policy for the conversation
// loop. It owns no tool implementations; the shared agenttools registry is
// injected by the composition root.
package toolruntime

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/hzc19970630/rag-platform/internal/agenttools"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
)

type Runtime struct {
	db       *gorm.DB
	registry *agenttools.Registry
	enabled  bool
}

func New(db *gorm.DB, registry *agenttools.Registry, enabled bool) *Runtime {
	return &Runtime{db: db, registry: registry, enabled: enabled}
}

func (r *Runtime) Registry() *agenttools.Registry { return r.registry }

// Enabled applies the deployment kill switch, the knowledge-base override and,
// for auto, the presence of a table asset on the live document version.
func (r *Runtime) Enabled(ctx context.Context, scope authz.AuthorizationScope, knowledgeBaseID string) bool {
	if r == nil || !r.enabled || r.db == nil || r.registry == nil {
		return false
	}
	if len(r.registry.Specs()) == 0 {
		return false
	}
	var knowledgeBase models.KnowledgeBase
	err := tenantdb.MustScoped(r.db.WithContext(ctx), scope.TenantID).
		Select("table_qa_enabled").
		Where("id = ?", knowledgeBaseID).
		Take(&knowledgeBase).Error
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(knowledgeBase.TableQAEnabled)) {
	case "off":
		return false
	case "on":
		return true
	}
	var assets int64
	err = tenantdb.MustScoped(r.db.WithContext(ctx), scope.TenantID).
		Model(&models.TableAsset{}).
		Where("knowledge_base_id = ?", knowledgeBaseID).
		Where("EXISTS (SELECT 1 FROM documents d WHERE d.id = table_assets.document_id AND d.deleted_at IS NULL AND d.latest_version_id = table_assets.document_version_id)").
		Count(&assets).Error
	return err == nil && assets > 0
}
