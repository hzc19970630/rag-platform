// Package quality owns retrieval feedback: explicit answer ratings and implicit
// citation clicks. The rows feed the retrieval-quality loop; they never change
// retrieval results directly.
package quality

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

const (
	KindHelpful       = "helpful"
	KindUnhelpful     = "unhelpful"
	KindCitationClick = "citation_click"

	EvalStatusPassed    = "passed"
	EvalStatusFailed    = "failed"
	EvalStatusRegressed = "regressed"

	// regressionThreshold is the allowed drop before a new eval run is marked
	// regressed against the previous run of the same scope.
	regressionThreshold = 0.02
)

var comparedMetrics = []string{
	"documentHitRate", "recallAtK", "citationAccuracy", "groundedness", "refusalRate", "permissionNegativeDenials",
}

// SubmitInput is one feedback event. A rating targets the message; a citation
// click also carries the chunk that was opened.
type SubmitInput struct {
	Kind       string
	CitationID string
	ChunkID    string
}

type ChunkCount struct {
	ChunkID string
	Count   int64
}

// Summary is the read model for the quality view.
type Summary struct {
	Helpful        int64
	Unhelpful      int64
	CitationClicks int64
	HelpfulRate    float64
	Messages       int64
	TopUnhelpful   []ChunkCount
	Since          time.Time
	QualityStatus  string
	LatestEval     *models.RetrievalEvalRun
}

type Service struct {
	db      *gorm.DB
	metrics metrics.Recorder
}

func NewService(db *gorm.DB) *Service { return &Service{db: db, metrics: metrics.Nop{}} }

// SetMetrics wires the process recorder used to alert on regressions.
func (s *Service) SetMetrics(recorder metrics.Recorder) {
	if recorder != nil {
		s.metrics = recorder
	}
}

// EvalRunInput is one imported evaluation report.
type EvalRunInput struct {
	Kind            string
	KnowledgeBaseID string
	Status          string
	ConfigHash      string
	Metrics         map[string]float64
	Report          json.RawMessage
	CreatedBy       string
}

// RecordEvalRun stores a report and marks it regressed when a compared metric
// dropped by more than regressionThreshold from the previous run of the same
// kind and knowledge base.
func (s *Service) RecordEvalRun(ctx context.Context, tenantID string, input EvalRunInput) (models.RetrievalEvalRun, error) {
	kind := strings.TrimSpace(input.Kind)
	switch kind {
	case "m3", "rerank_compare", "manual":
	default:
		return models.RetrievalEvalRun{}, apperror.Invalidf("unsupported eval kind %q", input.Kind)
	}
	status := strings.TrimSpace(input.Status)
	if status != EvalStatusPassed && status != EvalStatusFailed {
		return models.RetrievalEvalRun{}, apperror.Invalidf("eval status must be passed or failed")
	}
	if len(input.Metrics) == 0 {
		return models.RetrievalEvalRun{}, apperror.Invalidf("eval metrics are required")
	}
	if len(input.Report) == 0 {
		input.Report = json.RawMessage(`{}`)
	}
	if !json.Valid(input.Report) {
		return models.RetrievalEvalRun{}, apperror.Invalidf("eval report is not valid JSON")
	}
	var previous models.RetrievalEvalRun
	previousQuery := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Where("tenant_id = ? AND kind = ? AND knowledge_base_id = ?", tenantID, kind, input.KnowledgeBaseID).
		Order("created_at DESC")
	if err := previousQuery.Take(&previous).Error; err != nil && err != gorm.ErrRecordNotFound {
		return models.RetrievalEvalRun{}, fmt.Errorf("load previous eval run: %w", err)
	}
	if status != EvalStatusFailed && previous.ID != "" {
		var prior map[string]float64
		if err := json.Unmarshal([]byte(previous.Metrics), &prior); err == nil {
			for _, key := range comparedMetrics {
				old, okOld := prior[key]
				current, okCurrent := input.Metrics[key]
				if okOld && okCurrent && current < old-regressionThreshold {
					status = EvalStatusRegressed
					break
				}
			}
		}
	}
	metricsJSON, err := json.Marshal(input.Metrics)
	if err != nil {
		return models.RetrievalEvalRun{}, fmt.Errorf("encode eval metrics: %w", err)
	}
	id, err := newEvalID()
	if err != nil {
		return models.RetrievalEvalRun{}, err
	}
	row := models.RetrievalEvalRun{
		ID: id, TenantID: tenantID, KnowledgeBaseID: input.KnowledgeBaseID, Kind: kind, Status: status,
		ConfigHash: input.ConfigHash, Metrics: string(metricsJSON), Report: string(input.Report),
		CreatedBy: input.CreatedBy, CreatedAt: time.Now(),
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return models.RetrievalEvalRun{}, fmt.Errorf("store eval run: %w", err)
	}
	if status == EvalStatusRegressed {
		s.metrics.Inc("retrieval_quality_regressions_total", metrics.Labels("kind", kind))
	}
	return row, nil
}

// RequireRecentEval enforces the high-risk change gate: a tenant or knowledge
// base must have an evaluation run within the last seven days.
func (s *Service) RequireRecentEval(ctx context.Context, tenantID, knowledgeBaseID string) error {
	query := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.RetrievalEvalRun{}).
		Where("tenant_id = ? AND created_at >= ?", tenantID, time.Now().Add(-7*24*time.Hour))
	if knowledgeBaseID != "" {
		query = query.Where("knowledge_base_id = '' OR knowledge_base_id = ?", knowledgeBaseID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return fmt.Errorf("check eval runs: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("%w: quality_eval_required: no evaluation run in the last 7 days", apperror.ErrConflict)
	}
	return nil
}

// LatestEvalRun returns the newest run for one scope.
func (s *Service) LatestEvalRun(ctx context.Context, tenantID, knowledgeBaseID, kind string) (*models.RetrievalEvalRun, error) {
	var row models.RetrievalEvalRun
	query := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Where("tenant_id = ?", tenantID)
	if knowledgeBaseID != "" {
		query = query.Where("knowledge_base_id = '' OR knowledge_base_id = ?", knowledgeBaseID)
	}
	if kind != "" {
		query = query.Where("kind = ?", kind)
	}
	if err := query.Order("created_at DESC").Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// Submit records one rating or citation click. Ratings are one row per actor
// and message; repeated clicks on the same chunk are idempotent.
func (s *Service) Submit(ctx context.Context, principal authz.Principal, conversationID, messageID string, input SubmitInput) (models.RetrievalFeedback, error) {
	kind := strings.TrimSpace(input.Kind)
	switch kind {
	case KindHelpful, KindUnhelpful, KindCitationClick:
	default:
		return models.RetrievalFeedback{}, apperror.Invalidf("unsupported feedback kind %q", input.Kind)
	}
	if kind == KindCitationClick && strings.TrimSpace(input.ChunkID) == "" {
		return models.RetrievalFeedback{}, apperror.Invalidf("citation click requires a chunkId")
	}
	var conversation models.Conversation
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("id = ? AND tenant_id = ? AND user_id = ?", conversationID, principal.TenantID, principal.SubjectID).
		Take(&conversation).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return models.RetrievalFeedback{}, apperror.ErrNotFound
		}
		return models.RetrievalFeedback{}, fmt.Errorf("load conversation: %w", err)
	}
	var message models.Message
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), principal.TenantID).
		Where("id = ? AND conversation_id = ? AND tenant_id = ? AND role = ?", messageID, conversationID, principal.TenantID, "assistant").
		Take(&message).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return models.RetrievalFeedback{}, apperror.ErrNotFound
		}
		return models.RetrievalFeedback{}, fmt.Errorf("load message: %w", err)
	}
	var saved models.RetrievalFeedback
	err := tenantdb.InTx(ctx, s.db, principal.TenantID, func(tx *gorm.DB) error {
		now := time.Now()
		query := tenantdb.MustScoped(tx, principal.TenantID).Where(
			"tenant_id = ? AND actor_id = ? AND message_id = ?", principal.TenantID, principal.SubjectID, messageID,
		)
		if kind == KindCitationClick {
			if err := query.Where("kind = ? AND chunk_id = ?", KindCitationClick, input.ChunkID).Take(&saved).Error; err == nil {
				return nil
			} else if err != gorm.ErrRecordNotFound {
				return err
			}
		} else {
			if err := query.Where("kind IN ?", []string{KindHelpful, KindUnhelpful}).Take(&saved).Error; err == nil {
				return tenantdb.MustScoped(tx, principal.TenantID).Model(&models.RetrievalFeedback{}).Where("id = ?", saved.ID).
					Updates(map[string]any{
						"kind": kind, "citation_id": input.CitationID, "chunk_id": input.ChunkID, "updated_at": now,
					}).Error
			} else if err != gorm.ErrRecordNotFound {
				return err
			}
		}
		id, err := newID()
		if err != nil {
			return err
		}
		saved = models.RetrievalFeedback{
			ID: id, TenantID: principal.TenantID, KnowledgeBaseID: message.KnowledgeBaseID,
			ConversationID: conversationID, MessageID: messageID, CitationID: input.CitationID,
			ChunkID: input.ChunkID, Kind: kind, ActorID: principal.SubjectID, CreatedAt: now, UpdatedAt: now,
		}
		return tx.Create(&saved).Error
	})
	if err != nil {
		return models.RetrievalFeedback{}, fmt.Errorf("store feedback: %w", err)
	}
	// The update path leaves the in-memory row stale; return the effective kind.
	saved.Kind = kind
	saved.CitationID = input.CitationID
	saved.ChunkID = input.ChunkID
	return saved, nil
}

// Summary aggregates feedback for one tenant, optionally one knowledge base.
func (s *Service) Summary(ctx context.Context, tenantID, knowledgeBaseID string, since time.Time) (Summary, error) {
	result := Summary{Since: since, QualityStatus: "no_eval"}
	query := func() *gorm.DB {
		db := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.RetrievalFeedback{}).
			Where("tenant_id = ? AND created_at >= ?", tenantID, since)
		if knowledgeBaseID != "" {
			db = db.Where("knowledge_base_id = ?", knowledgeBaseID)
		}
		return db
	}
	if err := query().Where("kind = ?", KindHelpful).Count(&result.Helpful).Error; err != nil {
		return Summary{}, fmt.Errorf("count helpful feedback: %w", err)
	}
	if err := query().Where("kind = ?", KindUnhelpful).Count(&result.Unhelpful).Error; err != nil {
		return Summary{}, fmt.Errorf("count unhelpful feedback: %w", err)
	}
	if err := query().Where("kind = ?", KindCitationClick).Count(&result.CitationClicks).Error; err != nil {
		return Summary{}, fmt.Errorf("count citation clicks: %w", err)
	}
	if err := query().Distinct("message_id").Count(&result.Messages).Error; err != nil {
		return Summary{}, fmt.Errorf("count rated messages: %w", err)
	}
	rated := result.Helpful + result.Unhelpful
	if rated > 0 {
		result.HelpfulRate = float64(result.Helpful) / float64(rated)
	}
	var top []ChunkCount
	if err := query().Select("chunk_id, count(*) AS count").
		Where("kind = ? AND chunk_id <> ''", KindUnhelpful).
		Group("chunk_id").Order("count DESC").Limit(5).Scan(&top).Error; err != nil {
		return Summary{}, fmt.Errorf("load unhelpful chunks: %w", err)
	}
	result.TopUnhelpful = top
	if latest, err := s.LatestEvalRun(ctx, tenantID, knowledgeBaseID, ""); err != nil {
		return Summary{}, err
	} else if latest != nil {
		result.LatestEval = latest
		result.QualityStatus = latest.Status
	}
	return result, nil
}

func newEvalID() (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate eval id: %w", err)
	}
	return "eval-" + hex.EncodeToString(buffer), nil
}

func newID() (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate feedback id: %w", err)
	}
	return "fb-" + hex.EncodeToString(buffer), nil
}
