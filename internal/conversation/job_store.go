package conversation

import (
	"context"
	"fmt"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// StaleJob is the minimal identity the sweep needs to append a terminal event
// to a job whose worker disappeared.
type StaleJob struct {
	ID        string
	TenantID  string
	UserID    string
	RequestID string
}

// AnswerJobStore is the narrow persistence port for the async answer hand-off.
// It owns the conditional claim, the lease fields and the shared event
// sequence; the service owns every state decision.
type AnswerJobStore interface {
	CreateOrLoad(ctx context.Context, job models.AnswerJob) (models.AnswerJob, bool, error)
	Claim(ctx context.Context, jobID, tenantID string, leaseUntil, now time.Time) (models.AnswerJob, bool, error)
	Exists(ctx context.Context, jobID, tenantID string) (bool, error)
	Load(ctx context.Context, jobID string) (models.AnswerJob, error)
	Update(ctx context.Context, jobID, tenantID string, values map[string]any) error
	SweepStale(ctx context.Context, now time.Time, limit int) ([]StaleJob, error)
	NextSequence(ctx context.Context, jobID, tenantID string) (int, error)
}

type PostgresAnswerJobStore struct {
	db *gorm.DB
}

func NewPostgresAnswerJobStore(db *gorm.DB) *PostgresAnswerJobStore {
	return &PostgresAnswerJobStore{db: db}
}

func (s *PostgresAnswerJobStore) CreateOrLoad(ctx context.Context, job models.AnswerJob) (models.AnswerJob, bool, error) {
	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&job)
	if result.Error != nil {
		return models.AnswerJob{}, false, fmt.Errorf("create answer job: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return job, true, nil
	}
	var existing models.AnswerJob
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), job.TenantID).
		Where("user_id = ? AND conversation_id = ? AND request_id = ?",
			job.UserID, job.ConversationID, job.RequestID).
		First(&existing).Error; err != nil {
		return models.AnswerJob{}, false, fmt.Errorf("load concurrent answer job: %w", err)
	}
	return existing, false, nil
}

func (s *PostgresAnswerJobStore) Claim(ctx context.Context, jobID, tenantID string, leaseUntil, now time.Time) (models.AnswerJob, bool, error) {
	var job models.AnswerJob
	err := s.db.WithContext(ctx).Raw(`
		UPDATE answer_jobs
		SET status = ?, attempts = attempts + 1, lease_owner = ?, lease_until = ?, updated_at = ?
		WHERE id = ? AND tenant_id = ?
		  AND (status = ? OR (status = ? AND (lease_until IS NULL OR lease_until < ?)))
		RETURNING id, tenant_id, user_id, conversation_id, request_id, status, attempts,
		          lease_owner, lease_until, next_sequence, scope_json, error_code, created_at, updated_at`,
		AnswerJobRunning, jobID, leaseUntil, now,
		jobID, tenantID,
		AnswerJobQueued, AnswerJobRunning, now,
	).Scan(&job).Error
	if err != nil {
		return models.AnswerJob{}, false, fmt.Errorf("claim answer job: %w", err)
	}
	if job.ID == "" {
		return models.AnswerJob{}, false, nil
	}
	return job, true, nil
}

func (s *PostgresAnswerJobStore) Exists(ctx context.Context, jobID, tenantID string) (bool, error) {
	var exists bool
	if err := s.db.WithContext(ctx).Raw(
		"SELECT EXISTS (SELECT 1 FROM answer_jobs WHERE id = ? AND tenant_id = ?)",
		jobID, tenantID,
	).Scan(&exists).Error; err != nil {
		return false, fmt.Errorf("probe answer job: %w", err)
	}
	return exists, nil
}

func (s *PostgresAnswerJobStore) Load(ctx context.Context, jobID string) (models.AnswerJob, error) {
	var job models.AnswerJob
	if err := s.db.WithContext(ctx).Where("id = ?", jobID).First(&job).Error; err != nil {
		return models.AnswerJob{}, fmt.Errorf("load answer job %s: %w", jobID, err)
	}
	return job, nil
}

func (s *PostgresAnswerJobStore) Update(ctx context.Context, jobID, tenantID string, values map[string]any) error {
	if err := s.db.WithContext(ctx).Model(&models.AnswerJob{}).
		Where("id = ? AND tenant_id = ?", jobID, tenantID).Updates(values).Error; err != nil {
		return fmt.Errorf("update answer job %s: %w", jobID, err)
	}
	return nil
}

func (s *PostgresAnswerJobStore) SweepStale(ctx context.Context, now time.Time, limit int) ([]StaleJob, error) {
	var rows []StaleJob
	// This sweep is deliberately cross-tenant: it runs in the maintenance loop
	// on the BYPASSRLS role and only touches another tenant's job when its lease
	// has expired.
	err := s.db.WithContext(ctx).Raw(`
		UPDATE answer_jobs SET status = ?, error_code = ?, lease_owner = '', lease_until = NULL, updated_at = ?
		WHERE id IN (
			SELECT id FROM answer_jobs
			WHERE status = ? AND lease_until IS NOT NULL AND lease_until < ?
			ORDER BY lease_until LIMIT ?
		)
		RETURNING id, tenant_id, user_id, request_id`,
		AnswerJobFailed, CodeWorkerLost, now, AnswerJobRunning, now, limit).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("sweep stale answer jobs: %w", err)
	}
	return rows, nil
}

func (s *PostgresAnswerJobStore) NextSequence(ctx context.Context, jobID, tenantID string) (int, error) {
	var sequence int
	if err := s.db.WithContext(ctx).Raw(
		`UPDATE answer_jobs SET next_sequence = next_sequence + 1, updated_at = ? WHERE id = ? AND tenant_id = ? RETURNING next_sequence`,
		time.Now(), jobID, tenantID,
	).Scan(&sequence).Error; err != nil {
		return 0, fmt.Errorf("allocate answer event sequence: %w", err)
	}
	return sequence, nil
}
