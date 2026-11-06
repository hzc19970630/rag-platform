package knowledgeprojection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/contracts/evidence"
	"github.com/example/rag-platform/internal/contracts/metrics"
	"github.com/example/rag-platform/internal/contracts/projectioncache"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/pipeline"
	"github.com/example/rag-platform/internal/platform/evidencestore"
	"github.com/example/rag-platform/internal/platform/persistence/inboxlease"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/relationstore"
	"gorm.io/gorm"
)

// Authorizer is the slice of the authorization service used by projection and
// graph queries. *authz.Service satisfies it; tests can substitute a fake to
// exercise per-chunk denial, which the current KB-level ACL cannot express.
type Authorizer interface {
	Authorize(ctx context.Context, principal authz.Principal, action, object string) (bool, error)
	AuthorizeKnowledgeBase(ctx context.Context, principal authz.Principal, knowledgeBaseID, action string) (bool, error)
	BuildScope(ctx context.Context, principal authz.Principal, target authz.QueryTarget) (authz.AuthorizationScope, error)
	FilterReadable(ctx context.Context, scope authz.AuthorizationScope, refs []authz.ResourceRef) ([]authz.ResourceRef, error)
}

type Options struct {
	DB        *gorm.DB
	Authz     Authorizer
	Embedder  ingestion.Embedder
	Generator *Generator
	Extractor *Extractor
	Publisher Publisher
	Timeout   time.Duration
	// Store is the Relation Store port. Wiki-only deployments may leave it
	// nil; graph operations then fail with an explicit configuration error.
	Store relationstore.RelationStore
	// Evidence is the read port for published EvidencePackages. When omitted, a
	// PostgreSQL reader over DB is used.
	Evidence evidence.Reader
	// Cache is the per-package generation cache. Nil keeps full generation.
	Cache projectioncache.Cache
	// Metrics records cache hit/miss package counts.
	Metrics metrics.Recorder
}

type Service struct {
	db         *gorm.DB
	authz      Authorizer
	embedder   ingestion.Embedder
	generator  *Generator
	relation   *Extractor
	publisher  Publisher
	timeout    time.Duration
	store      relationstore.RelationStore
	evidence   evidence.Reader
	cache      projectioncache.Cache
	metrics    metrics.Recorder
	leaseOwner inboxlease.Owner
	leaseTTL   time.Duration
}

func (s *Service) runtime() *pipeline.Runtime {
	return pipeline.NewRuntime(s.db, &s.leaseOwner, s.leaseTTL)
}

func NewService(options Options) *Service {
	reader := options.Evidence
	if reader == nil && options.DB != nil {
		reader = evidencestore.New(options.DB)
	}
	recorder := options.Metrics
	if recorder == nil {
		recorder = metrics.Nop{}
	}
	if options.Generator != nil {
		options.Generator.SetCache(options.Cache)
	}
	if options.Extractor != nil {
		options.Extractor.SetCache(options.Cache)
	}
	return &Service{
		db: options.DB, authz: options.Authz, embedder: options.Embedder,
		generator: options.Generator, relation: options.Extractor,
		publisher: options.Publisher, timeout: options.Timeout, store: options.Store,
		evidence: reader, cache: options.Cache, metrics: recorder,
	}
}

// recordGeneration reports how many packages a projection run regenerated
// versus reused. It is deliberately best-effort: observability must never fail
// a projection.
func (s *Service) recordGeneration(projectionType string, generated, reused int) {
	slog.Info("projection_generation",
		"projection_type", projectionType, "generated", generated, "reused", reused)
	if s.metrics == nil {
		return
	}
	record := func(name string, value int) {
		if value <= 0 {
			return
		}
		if adder, ok := s.metrics.(metrics.Adder); ok {
			adder.Add(name, int64(value), metrics.Labels("type", projectionType))
			return
		}
		for index := 0; index < value; index++ {
			s.metrics.Inc(name, metrics.Labels("type", projectionType))
		}
	}
	record("projection_cache_misses_total", generated)
	record("projection_cache_hits_total", reused)
}

// clearGenerationCache makes a manual rebuild a real regeneration. Automatic
// triggers keep the cache so unchanged packages stay reusable.
func (s *Service) clearGenerationCache(ctx context.Context, tenantID, knowledgeBaseID, projectionType string) error {
	if s.cache == nil {
		return nil
	}
	if err := s.cache.DeleteScope(ctx, tenantID, knowledgeBaseID, projectionType); err != nil {
		return fmt.Errorf("clear %s generation cache: %w", projectionType, err)
	}
	return nil
}

// PurgeGenerationCache removes cache rows whose EvidencePackage no longer
// exists. The optional interface keeps the generator-facing Cache contract
// small while the PostgreSQL adapter supplies the maintenance sweep.
func (s *Service) PurgeGenerationCache(ctx context.Context, limit int) (int64, error) {
	if s.cache == nil {
		return 0, nil
	}
	type purger interface {
		PurgeOrphans(context.Context, int) (int64, error)
	}
	store, ok := s.cache.(purger)
	if !ok {
		return 0, nil
	}
	return store.PurgeOrphans(ctx, limit)
}

func (s *Service) requireReviewer(ctx context.Context, principal authz.Principal, knowledgeBaseID string) (authz.AuthorizationScope, error) {
	if principal.CredentialType == "api_key" {
		return authz.AuthorizationScope{}, apperror.ErrForbidden
	}
	return s.authorize(ctx, principal, knowledgeBaseID, "manage_members")
}

func (s *Service) authorize(ctx context.Context, principal authz.Principal, knowledgeBaseID, action string) (authz.AuthorizationScope, error) {
	if s.authz == nil {
		return authz.AuthorizationScope{}, apperror.ErrForbidden
	}
	scope, err := s.authz.BuildScope(ctx, principal, authz.QueryTarget{KnowledgeBaseID: knowledgeBaseID})
	if err != nil {
		return authz.AuthorizationScope{}, err
	}
	allowed, err := s.authz.AuthorizeKnowledgeBase(ctx, principal, knowledgeBaseID, action)
	if err != nil {
		return authz.AuthorizationScope{}, err
	}
	if !allowed {
		return authz.AuthorizationScope{}, apperror.ErrForbidden
	}
	return scope, nil
}

func (s *Service) recordAudit(ctx context.Context, principal authz.Principal, knowledgeBaseID, resourceID, reason string) {
	s.recordAuditResource(ctx, principal, knowledgeBaseID, "wiki_page", resourceID, reason)
}

func (s *Service) recordAuditResource(ctx context.Context, principal authz.Principal, knowledgeBaseID, resourceType, resourceID, reason string) {
	if s.db == nil {
		return
	}
	_ = s.db.WithContext(ctx).Create(&models.AuditLog{
		TenantID: principal.TenantID, ActorID: principal.SubjectID, Action: "write",
		ResourceType: resourceType, ResourceID: resourceID, Allowed: true, Reason: reason + ":" + knowledgeBaseID,
		RequestID: principal.RequestID, IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}).Error
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
