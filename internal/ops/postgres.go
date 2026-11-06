package ops

import (
	"context"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

type PostgresStore struct {
	db *gorm.DB
}

func NewPostgresStore(db *gorm.DB) *PostgresStore { return &PostgresStore{db: db} }

// Snapshot reports tenant-scoped signals the process-local registry cannot see.
// The caller supplies the window so an old incident does not alert forever.
func (s *PostgresStore) Snapshot(ctx context.Context, tenantID string, since time.Time, outboxMaxAttempts int) (Snapshot, error) {
	var stageRows []struct {
		Stage  string
		Status string
		Total  int64
	}
	if err := s.db.WithContext(ctx).
		Table("stage_runs s").
		Select("s.stage, s.status, count(*) AS total").
		Joins("JOIN pipeline_runs p ON p.id = s.pipeline_run_id").
		Where("p.tenant_id = ? AND s.updated_at >= ?", tenantID, since).
		Group("s.stage, s.status").
		Scan(&stageRows).Error; err != nil {
		return Snapshot{}, err
	}
	stages := map[string]map[string]int64{}
	for _, row := range stageRows {
		if stages[row.Stage] == nil {
			stages[row.Stage] = map[string]int64{}
		}
		stages[row.Stage][strings.ToLower(row.Status)] = row.Total
	}
	parse := stages["parse"]
	denominator := parse["succeeded"] + parse["failed"]
	failureRate := 0.0
	if denominator > 0 {
		failureRate = float64(parse["failed"]) / float64(denominator)
	}

	var modelErrors int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.Message{}).
		Where("role = ? AND status = ? AND created_at >= ?", "assistant", "FAILED", since).
		Count(&modelErrors).Error; err != nil {
		return Snapshot{}, err
	}

	var letterRows []struct {
		Status string
		Total  int64
	}
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Table("task_dead_letters").
		Select("status, count(*) AS total").
		Where("status IN ?", []string{"PENDING", "RETRYING"}).
		Group("status").
		Scan(&letterRows).Error; err != nil {
		return Snapshot{}, err
	}
	deadLetters := map[string]int64{}
	for _, row := range letterRows {
		deadLetters[row.Status] = row.Total
	}

	var backlog int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.OutboxEvent{}).
		Where("published_at IS NULL AND attempts < ?", outboxMaxAttempts).
		Count(&backlog).Error; err != nil {
		return Snapshot{}, err
	}

	return Snapshot{
		Stages:           stages,
		ParseFailureRate: failureRate,
		ModelErrors24h:   modelErrors,
		DeadLetters:      deadLetters,
		OutboxBacklog:    backlog,
	}, nil
}
