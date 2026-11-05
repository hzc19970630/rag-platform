package knowledgeprojection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/contracts/evidence"
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
	return &Service{
		db: options.DB, authz: options.Authz, embedder: options.Embedder,
		generator: options.Generator, relation: options.Extractor,
		publisher: options.Publisher, timeout: options.Timeout, store: options.Store,
		evidence: reader,
	}
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
	allowed, err := s.authz.Authorize(ctx, principal, action, "/knowledge-bases/"+knowledgeBaseID)
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
