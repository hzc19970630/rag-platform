// Package ops owns operator-facing snapshots built from durable tables. The
// HTTP transport stays thin: it authenticates, calls this service and serializes.
package ops

import (
	"context"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
)

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

type Snapshot struct {
	Stages           map[string]map[string]int64 `json:"stages"`
	ParseFailureRate float64                     `json:"parseFailureRate"`
	ModelErrors24h   int64                       `json:"modelErrors24h"`
	DeadLetters      map[string]int64            `json:"deadLetters"`
	OutboxBacklog    int64                       `json:"outboxBacklog"`
}

// Snapshot reports tenant-scoped signals the process-local registry cannot see.
// The caller supplies the window so an old incident does not alert forever.
func (s *Service) Snapshot(ctx context.Context, tenantID string, since time.Time, outboxMaxAttempts int) (Snapshot, error) {
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
	if err := s.db.WithContext(ctx).Model(&models.Message{}).
		Where("tenant_id = ? AND role = ? AND status = ? AND created_at >= ?", tenantID, "assistant", "FAILED", since).
		Count(&modelErrors).Error; err != nil {
		return Snapshot{}, err
	}

	var letterRows []struct {
		Status string
		Total  int64
	}
	if err := s.db.WithContext(ctx).
		Table("task_dead_letters").
		Select("status, count(*) AS total").
		Where("tenant_id = ? AND status IN ?", tenantID, []string{"PENDING", "RETRYING"}).
		Group("status").
		Scan(&letterRows).Error; err != nil {
		return Snapshot{}, err
	}
	deadLetters := map[string]int64{}
	for _, row := range letterRows {
		deadLetters[row.Status] = row.Total
	}

	var backlog int64
	if err := s.db.WithContext(ctx).Model(&models.OutboxEvent{}).
		Where("tenant_id = ? AND published_at IS NULL AND attempts < ?", tenantID, outboxMaxAttempts).
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
