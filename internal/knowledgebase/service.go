package knowledgebase

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
	"gorm.io/gorm"
)

type Service struct {
	db           *gorm.DB
	authz        *authz.Service
	projection   ProjectionLifecycle
	embedding    EmbeddingBinding
	catalog      ModelCatalog
	tenantParams TenantParamsResolver
}

// TenantParamsResolver loads the tenant tier of the retrieval override chain.
// Nil keeps the deployment-plus-knowledge-base view used by tests.
type TenantParamsResolver interface {
	TenantRetrievalOverride(ctx context.Context, tenantID string) (string, error)
}

// ModelCatalog is the deployment and tenant model catalog. The knowledge-base
// service validates bindings against it but does not know which adapters
// implement the models, so a binding can never name a model this deployment
// cannot serve.
type ModelCatalog interface {
	ResolveBinding(ctx context.Context, tenantID string, capability model.Capability, requested string) (model.ModelRef, error)
	// ResolveEmbeddingModel returns the vector dimension a registered embedding
	// model produces, so a binding write cannot name a model with an
	// incompatible dimension.
	ResolveEmbeddingModel(ctx context.Context, tenantID, requested string) (model.EmbeddingBinding, error)
}

// EmbeddingBinding is the model and dimension this deployment writes and
// queries. A knowledge base pins the pair at creation so vectors from two
// deployments can never be compared silently.
type EmbeddingBinding struct {
	Model     string
	Dimension int
}

type CreateInput struct {
	Name        string `json:"name" binding:"required,min=1,max=255"`
	Description string `json:"description"`
}

type UpdateInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	// RetrievalConfig is the knowledge-base tier of the retrieval parameter
	// override chain. It is stored in the database because it must be
	// runtime-changeable, tenant-scoped and audited.
	RetrievalConfig *retrieval.ParamsOverride `json:"retrieval_config"`
	// IndexingStrategy holds the IndexingStrategyV1 capability switches. Partial
	// updates replace the strategy as a whole so the four switches cannot drift
	// into an inconsistent combination.
	IndexingStrategy *retrieval.IndexingStrategy `json:"indexing_strategy"`
	// EmbeddingModel and EmbeddingDimension are accepted only to rebind an empty
	// knowledge base to this deployment's binding. Once documents exist the pair
	// is locked; the managed rebuild is the only path that moves it.
	EmbeddingModel     *string `json:"embedding_model"`
	EmbeddingDimension *int    `json:"embedding_dimension"`
	// ChatModel and RerankModel select a model registered by this deployment.
	// Empty inherits the deployment default. Both are runtime-changeable and
	// audited; embedding stays locked because it defines the vector contract.
	ChatModel   *string `json:"chat_model"`
	RerankModel *string `json:"rerank_model"`
}

// ProjectionLifecycle is the Pipeline B hook. It is separate from Pipeline A:
// invalidating or rebuilding Wiki pages or graph snapshots never changes
// Evidence Index rows.
type ProjectionLifecycle interface {
	InvalidateKnowledgeBase(ctx context.Context, tenantID, knowledgeBaseID, reason string) error
	RebuildKnowledgeBase(ctx context.Context, tenantID, knowledgeBaseID string) (int, error)
	RebuildRelations(ctx context.Context, tenantID, knowledgeBaseID string) (int, error)
}

type AddMemberInput struct {
	UserID string `json:"user_id" binding:"required"`
	Role   string `json:"role" binding:"required"`
}

func NewService(db *gorm.DB, authorizer *authz.Service, embedding EmbeddingBinding) *Service {
	return &Service{db: db, authz: authorizer, embedding: embedding}
}

// SetProjectionLifecycle wires Pipeline B without making the knowledge-base
// service depend on the projection package.
func (s *Service) SetProjectionLifecycle(lifecycle ProjectionLifecycle) {
	s.projection = lifecycle
}

// SetModelCatalog wires model binding validation without coupling the service
// to the registry implementation.
func (s *Service) SetModelCatalog(catalog ModelCatalog) {
	s.catalog = catalog
}

// SetTenantParamsResolver wires the tenant tier read without coupling the
// service to the configuration store.
func (s *Service) SetTenantParamsResolver(resolver TenantParamsResolver) {
	s.tenantParams = resolver
}

func (s *Service) Create(ctx context.Context, principal authz.Principal, input CreateInput) (models.KnowledgeBase, error) {
	if strings.TrimSpace(input.Name) == "" {
		return models.KnowledgeBase{}, apperror.Invalidf("name is required")
	}
	allowed, err := s.authz.AuthorizeKnowledgeBases(ctx, principal, "write")
	if err != nil {
		return models.KnowledgeBase{}, err
	}
	if !allowed {
		return models.KnowledgeBase{}, apperror.ErrForbidden
	}
	id, err := newID("kb")
	if err != nil {
		return models.KnowledgeBase{}, err
	}
	knowledgeBase := models.KnowledgeBase{ID: id, TenantID: principal.TenantID, OwnerID: principal.SubjectID, Name: input.Name, Description: input.Description, EmbeddingModel: s.embedding.Model, EmbeddingDimension: s.embedding.Dimension, ACLVersion: 1}
	err = tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		if err := tx.Create(&knowledgeBase).Error; err != nil {
			return err
		}
		return tx.Create(&models.KnowledgeBaseMember{KnowledgeBaseID: id, UserID: principal.SubjectID, Role: "owner"}).Error
	})
	if err != nil {
		return models.KnowledgeBase{}, fmt.Errorf("create knowledge base: %w", err)
	}
	if err := s.authz.BumpRevision(ctx, principal.TenantID); err != nil {
		return models.KnowledgeBase{}, err
	}
	return knowledgeBase, nil
}

// BackfillEmbeddingBinding adopts the deployment binding for knowledge bases
// created before the binding columns existed. An existing binding is never
// rewritten here: the managed rebuild is the only path that moves it.
func BackfillEmbeddingBinding(ctx context.Context, db *gorm.DB, binding EmbeddingBinding) (int64, error) {
	if binding.Model == "" || binding.Dimension <= 0 {
		return 0, nil
	}
	result := tenantdb.Unscoped(db.WithContext(ctx), "startup: backfill embedding binding for pre-upgrade knowledge bases").
		Model(&models.KnowledgeBase{}).
		Where("embedding_model = ''").
		Updates(map[string]any{
			"embedding_model":     binding.Model,
			"embedding_dimension": binding.Dimension,
			"updated_at":          time.Now(),
		})
	if result.Error != nil {
		return 0, fmt.Errorf("backfill knowledge base embedding binding: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (s *Service) List(ctx context.Context, principal authz.Principal) ([]models.KnowledgeBase, error) {
	scope, err := s.authz.BuildScope(ctx, principal, authz.QueryTarget{})
	if err != nil {
		return nil, err
	}
	if len(scope.KnowledgeBaseIDs) == 0 {
		return []models.KnowledgeBase{}, nil
	}
	var rows []models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id IN ?", scope.KnowledgeBaseIDs).Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list knowledge bases: %w", err)
	}
	return rows, nil
}

func (s *Service) Get(ctx context.Context, principal authz.Principal, id string) (models.KnowledgeBase, error) {
	if _, err := s.authorizedScope(ctx, principal, id, "read"); err != nil {
		return models.KnowledgeBase{}, err
	}
	var row models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", id).First(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return models.KnowledgeBase{}, apperror.ErrNotFound
		}
		return models.KnowledgeBase{}, fmt.Errorf("get knowledge base: %w", err)
	}
	return row, nil
}

func (s *Service) Update(ctx context.Context, principal authz.Principal, id string, input UpdateInput) (models.KnowledgeBase, error) {
	if _, err := s.authorizedScope(ctx, principal, id, "write"); err != nil {
		return models.KnowledgeBase{}, err
	}
	var row models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", id).First(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return models.KnowledgeBase{}, apperror.ErrNotFound
		}
		return models.KnowledgeBase{}, fmt.Errorf("load knowledge base: %w", err)
	}
	if input.Name != nil {
		if strings.TrimSpace(*input.Name) == "" {
			return models.KnowledgeBase{}, apperror.Invalidf("name cannot be empty")
		}
		row.Name = *input.Name
	}
	if input.Description != nil {
		row.Description = *input.Description
	}
	if input.EmbeddingModel != nil || input.EmbeddingDimension != nil {
		next := EmbeddingBinding{Model: row.EmbeddingModel, Dimension: row.EmbeddingDimension}
		if input.EmbeddingModel != nil {
			next.Model = strings.TrimSpace(*input.EmbeddingModel)
		}
		if input.EmbeddingDimension != nil {
			next.Dimension = *input.EmbeddingDimension
		}
		if s.catalog == nil {
			// Without a catalog only the deployment space is known.
			if next.Model == "" {
				next.Model = s.embedding.Model
			}
			if next.Model != s.embedding.Model || (next.Dimension != 0 && next.Dimension != s.embedding.Dimension) {
				return models.KnowledgeBase{}, apperror.Invalidf("unsupported embedding binding %q/%d: this deployment serves %q/%d", next.Model, next.Dimension, s.embedding.Model, s.embedding.Dimension)
			}
			next.Dimension = s.embedding.Dimension
		} else {
			resolved, err := s.catalog.ResolveEmbeddingModel(ctx, principal.TenantID, next.Model)
			if err != nil {
				return models.KnowledgeBase{}, err
			}
			if input.EmbeddingDimension != nil && *input.EmbeddingDimension != resolved.Dimension {
				return models.KnowledgeBase{}, apperror.Invalidf("embedding dimension %d does not match model %q dimension %d", *input.EmbeddingDimension, resolved.Model, resolved.Dimension)
			}
			next = EmbeddingBinding{Model: resolved.Model, Dimension: resolved.Dimension}
		}
		if next != (EmbeddingBinding{Model: row.EmbeddingModel, Dimension: row.EmbeddingDimension}) {
			// An empty binding is a pre-upgrade row, not a contract; adopting the
			// deployment binding is safe even when documents already exist.
			if row.EmbeddingModel != "" {
				// Soft-deleted documents are still restorable, so they keep their
				// vectors and must count as evidence that the binding is in use.
				var documents int64
				if err := s.db.WithContext(ctx).Model(&models.Document{}).
					Where("tenant_id = ? AND knowledge_base_id = ?", principal.TenantID, id).
					Count(&documents).Error; err != nil {
					return models.KnowledgeBase{}, fmt.Errorf("count knowledge base documents: %w", err)
				}
				if documents > 0 {
					return models.KnowledgeBase{}, fmt.Errorf("embedding binding cannot be changed once documents exist")
				}
			}
			row.EmbeddingModel = next.Model
			row.EmbeddingDimension = next.Dimension
		}
	}
	bindingAudits := make([]modelBindingAudit, 0, 2)
	if input.ChatModel != nil {
		audit, next, err := s.nextModelBinding(ctx, principal.TenantID, model.CapabilityChat, *input.ChatModel, row.ChatModel, row.ChatModelVersion)
		if err != nil {
			return models.KnowledgeBase{}, err
		}
		if audit != nil {
			row.ChatModel, row.ChatModelVersion = next.Model, next.Version
			bindingAudits = append(bindingAudits, *audit)
		}
	}
	if input.RerankModel != nil {
		audit, next, err := s.nextModelBinding(ctx, principal.TenantID, model.CapabilityRerank, *input.RerankModel, row.RerankModel, row.RerankModelVersion)
		if err != nil {
			return models.KnowledgeBase{}, err
		}
		if audit != nil {
			row.RerankModel, row.RerankModelVersion = next.Model, next.Version
			bindingAudits = append(bindingAudits, *audit)
		}
	}
	if input.RetrievalConfig != nil {
		if err := retrieval.ValidateOverride(*input.RetrievalConfig); err != nil {
			return models.KnowledgeBase{}, err
		}
		existing, err := retrieval.ParseOverride(row.RetrievalConfig)
		if err != nil {
			return models.KnowledgeBase{}, err
		}
		merged := existing.Overlay(*input.RetrievalConfig)
		encoded, err := json.Marshal(merged)
		if err != nil {
			return models.KnowledgeBase{}, fmt.Errorf("encode retrieval config: %w", err)
		}
		row.RetrievalConfig = string(encoded)
	}
	projectionChanged := false
	if input.IndexingStrategy != nil {
		previous, err := retrieval.ParseIndexingStrategy(row.IndexingStrategy)
		if err != nil {
			return models.KnowledgeBase{}, err
		}
		strategy := *input.IndexingStrategy
		if len(strategy.RequiredCapabilities) == 0 {
			strategy.RequiredCapabilities = []string{retrieval.CapabilityEvidence}
		}
		if err := retrieval.ValidateIndexingStrategy(strategy); err != nil {
			return models.KnowledgeBase{}, err
		}
		encoded, err := json.Marshal(strategy)
		if err != nil {
			return models.KnowledgeBase{}, fmt.Errorf("encode indexing strategy: %w", err)
		}
		row.IndexingStrategy = string(encoded)
		// Only projection-producing switches change derived content. The
		// vector/keyword channels affect the next retrieval plan but leave
		// existing pages, snapshots and their ACL version intact.
		projectionChanged = previous.WikiEnabled != strategy.WikiEnabled || previous.GraphEnabled != strategy.GraphEnabled
	}
	if projectionChanged {
		row.ACLVersion++
	}
	if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
		return models.KnowledgeBase{}, fmt.Errorf("update knowledge base: %w", err)
	}
	for _, audit := range bindingAudits {
		if err := s.recordModelBindingAudit(ctx, principal, id, audit); err != nil {
			return models.KnowledgeBase{}, err
		}
	}
	if input.RetrievalConfig != nil {
		if err := s.recordRetrievalConfigAudit(ctx, principal, id, row.RetrievalConfig); err != nil {
			return models.KnowledgeBase{}, err
		}
	}
	if input.IndexingStrategy != nil {
		if err := s.recordIndexingStrategyAudit(ctx, principal, id, row.IndexingStrategy); err != nil {
			return models.KnowledgeBase{}, err
		}
	}
	if projectionChanged {
		if err := s.refreshProjection(ctx, principal.TenantID, row, "indexing_strategy_updated"); err != nil {
			return models.KnowledgeBase{}, err
		}
	}
	return row, nil
}

// refreshProjection invalidates derived pages and graph snapshots after an
// ACL/revision change and starts fresh runs for the enabled projections. The
// invalidation happens first so a removed member can never read an old page or
// graph while regeneration is pending. The two rebuilds are independent: a
// relation failure must not prevent the Wiki rebuild.
func (s *Service) refreshProjection(ctx context.Context, tenantID string, knowledgeBase models.KnowledgeBase, reason string) error {
	if s.projection == nil {
		return nil
	}
	if err := s.projection.InvalidateKnowledgeBase(ctx, tenantID, knowledgeBase.ID, reason); err != nil {
		return fmt.Errorf("invalidate knowledge projection: %w", err)
	}
	strategy, err := retrieval.ParseIndexingStrategy(knowledgeBase.IndexingStrategy)
	if err != nil {
		return err
	}
	var rebuildErr error
	if strategy.WikiEnabled {
		if _, err := s.projection.RebuildKnowledgeBase(ctx, tenantID, knowledgeBase.ID); err != nil {
			rebuildErr = errors.Join(rebuildErr, fmt.Errorf("rebuild knowledge projection: %w", err))
		}
	}
	if strategy.GraphEnabled {
		if _, err := s.projection.RebuildRelations(ctx, tenantID, knowledgeBase.ID); err != nil {
			rebuildErr = errors.Join(rebuildErr, fmt.Errorf("rebuild relation projection: %w", err))
		}
	}
	return rebuildErr
}

type modelBindingAudit struct {
	Capability      model.Capability `json:"capability"`
	Model           string           `json:"model"`
	Provider        string           `json:"provider"`
	Version         string           `json:"version"`
	PreviousModel   string           `json:"previousModel"`
	PreviousVersion string           `json:"previousVersion"`
}

// nextModelBinding validates the requested model and reports whether the
// binding changed. An empty request clears the knowledge-base override so the
// row follows the deployment default again.
func (s *Service) nextModelBinding(ctx context.Context, tenantID string, capability model.Capability, requested, currentModel, currentVersion string) (*modelBindingAudit, model.ModelRef, error) {
	requested = strings.TrimSpace(requested)
	next := model.ModelRef{}
	if requested != "" {
		if s.catalog == nil {
			return nil, model.ModelRef{}, apperror.Invalidf("model registry is not configured")
		}
		resolved, err := s.catalog.ResolveBinding(ctx, tenantID, capability, requested)
		if err != nil {
			return nil, model.ModelRef{}, err
		}
		next = resolved
	}
	if next.Model == currentModel && next.Version == currentVersion {
		return nil, next, nil
	}
	return &modelBindingAudit{
		Capability: capability, Model: next.Model, Provider: next.Provider, Version: next.Version,
		PreviousModel: currentModel, PreviousVersion: currentVersion,
	}, next, nil
}

func (s *Service) recordModelBindingAudit(ctx context.Context, principal authz.Principal, id string, audit modelBindingAudit) error {
	encoded, err := json.Marshal(audit)
	if err != nil {
		return fmt.Errorf("encode model binding audit: %w", err)
	}
	return s.db.WithContext(ctx).Create(&models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: "write",
		ResourceType: "model_binding", ResourceID: id, Allowed: true, Reason: "model_binding_updated: " + string(encoded),
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}).Error
}

func (s *Service) recordIndexingStrategyAudit(ctx context.Context, principal authz.Principal, id, strategy string) error {
	return s.db.WithContext(ctx).Create(&models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: "write",
		ResourceType: "indexing_strategy", ResourceID: id, Allowed: true, Reason: "indexing_strategy_updated: " + strategy,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}).Error
}

// recordRetrievalConfigAudit records who changed the knowledge-base retrieval
// parameters and when, which is why the database tier exists rather than a
// plain settings table.
func (s *Service) recordRetrievalConfigAudit(ctx context.Context, principal authz.Principal, id, config string) error {
	return s.db.WithContext(ctx).Create(&models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: "write",
		ResourceType: "retrieval_config", ResourceID: id, Allowed: true, Reason: "retrieval_config_updated: " + config,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}).Error
}

func (s *Service) Delete(ctx context.Context, principal authz.Principal, id string) error {
	if _, err := s.authorizedScope(ctx, principal, id, "delete"); err != nil {
		return err
	}
	result := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", id).Delete(&models.KnowledgeBase{})
	if result.Error != nil {
		return fmt.Errorf("delete knowledge base: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return apperror.ErrNotFound
	}
	if err := s.db.WithContext(ctx).Where("knowledge_base_id = ?", id).Delete(&models.KnowledgeBaseMember{}).Error; err != nil {
		return fmt.Errorf("delete knowledge base members: %w", err)
	}
	return s.authz.BumpRevision(ctx, principal.TenantID)
}

func (s *Service) ListMembers(ctx context.Context, principal authz.Principal, id string) ([]models.KnowledgeBaseMember, error) {
	if _, err := s.authorizedScope(ctx, principal, id, "manage_members"); err != nil {
		return nil, err
	}
	var rows []models.KnowledgeBaseMember
	if err := s.db.WithContext(ctx).Where("knowledge_base_id = ?", id).Order("created_at").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list knowledge base members: %w", err)
	}
	return rows, nil
}

func (s *Service) AddMember(ctx context.Context, principal authz.Principal, id string, input AddMemberInput) (models.KnowledgeBaseMember, error) {
	if _, err := s.authorizedScope(ctx, principal, id, "manage_members"); err != nil {
		return models.KnowledgeBaseMember{}, err
	}
	if input.Role != "admin" && input.Role != "editor" && input.Role != "viewer" {
		return models.KnowledgeBaseMember{}, apperror.Invalidf("unsupported knowledge base role")
	}
	var knowledgeBase models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", id).First(&knowledgeBase).Error; err != nil {
		return models.KnowledgeBaseMember{}, apperror.ErrNotFound
	}
	var membership models.Membership
	if err := s.db.WithContext(ctx).First(&membership, "tenant_id = ? AND user_id = ?", knowledgeBase.TenantID, input.UserID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return models.KnowledgeBaseMember{}, apperror.ErrNotFound
		}
		return models.KnowledgeBaseMember{}, fmt.Errorf("load tenant member: %w", err)
	}
	member := models.KnowledgeBaseMember{KnowledgeBaseID: id, UserID: input.UserID, Role: input.Role}
	if err := tenantdb.InTx(ctx, s.db, knowledgeBase.TenantID, func(tx *gorm.DB) error {
		if err := tx.Save(&member).Error; err != nil {
			return err
		}
		return tenantdb.MustScoped(tx, principal.TenantID).Model(&models.KnowledgeBase{}).Where("id = ?", knowledgeBase.ID).UpdateColumn("acl_version", gorm.Expr("acl_version + 1")).Error
	}); err != nil {
		return models.KnowledgeBaseMember{}, fmt.Errorf("add knowledge base member: %w", err)
	}
	if err := s.authz.BumpRevision(ctx, principal.TenantID); err != nil {
		return models.KnowledgeBaseMember{}, err
	}
	if err := s.projectionInvalidate(ctx, principal.TenantID, id, "membership_changed"); err != nil {
		return models.KnowledgeBaseMember{}, err
	}
	return member, nil
}

func (s *Service) RemoveMember(ctx context.Context, principal authz.Principal, id, userID string) error {
	if _, err := s.authorizedScope(ctx, principal, id, "manage_members"); err != nil {
		return err
	}
	var knowledgeBase models.KnowledgeBase
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Where("id = ?", id).First(&knowledgeBase).Error; err != nil {
		return apperror.ErrNotFound
	}
	if knowledgeBase.OwnerID == userID {
		return fmt.Errorf("knowledge base owner cannot be removed")
	}
	result := s.db.WithContext(ctx).Where("knowledge_base_id = ? AND user_id = ?", id, userID).Delete(&models.KnowledgeBaseMember{})
	if result.Error != nil {
		return fmt.Errorf("remove knowledge base member: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return apperror.ErrNotFound
	}
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).Model(&models.KnowledgeBase{}).Where("id = ?", knowledgeBase.ID).UpdateColumn("acl_version", gorm.Expr("acl_version + 1")).Error; err != nil {
		return fmt.Errorf("update acl version: %w", err)
	}
	if err := s.authz.BumpRevision(ctx, principal.TenantID); err != nil {
		return err
	}
	return s.projectionInvalidate(ctx, principal.TenantID, id, "membership_changed")
}

func (s *Service) projectionInvalidate(ctx context.Context, tenantID, knowledgeBaseID, reason string) error {
	if s.projection == nil {
		return nil
	}
	if err := s.projection.InvalidateKnowledgeBase(ctx, tenantID, knowledgeBaseID, reason); err != nil {
		return fmt.Errorf("invalidate knowledge projection: %w", err)
	}
	return nil
}

func (s *Service) authorizedScope(ctx context.Context, principal authz.Principal, id, action string) (authz.AuthorizationScope, error) {
	scope, err := s.authz.BuildScope(ctx, principal, authz.QueryTarget{KnowledgeBaseID: id})
	if err != nil {
		return authz.AuthorizationScope{}, err
	}
	allowed, err := s.authz.AuthorizeKnowledgeBase(ctx, principal, id, action)
	if err != nil {
		return authz.AuthorizationScope{}, err
	}
	if !allowed {
		return authz.AuthorizationScope{}, apperror.ErrForbidden
	}
	return scope, nil
}

func newID(prefix string) (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(buffer), nil
}
