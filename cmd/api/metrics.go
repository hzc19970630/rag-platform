package main

import (
	"database/sql"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/observability"
	"gorm.io/gorm"
)

// dbPoolSampleInterval is coarse on purpose: pool saturation is slow-moving and
// the sample costs one lock acquisition.
const dbPoolSampleInterval = 15 * time.Second

// recordDBPoolStats publishes connection-pool occupancy. D9 (operation-level
// short transactions instead of one request-scoped transaction) is triggered
// when long-lived requests and SSE concurrency saturate the pool again.
func recordDBPoolStats(registry *observability.Registry, stats sql.DBStats) {
	if registry == nil {
		return
	}
	registry.SetGauge("db_pool_in_use", nil, float64(stats.InUse))
	registry.SetGauge("db_pool_max_open", nil, float64(stats.MaxOpenConnections))
	registry.SetGauge("db_pool_wait_count", nil, float64(stats.WaitCount))
	registry.SetGauge("db_pool_wait_seconds", nil, stats.WaitDuration.Seconds())
}

func watchDBPool(registry *observability.Registry, db *gorm.DB, done <-chan struct{}) {
	if db == nil {
		return
	}
	sqlDB, err := db.DB()
	if err != nil {
		return
	}
	ticker := time.NewTicker(dbPoolSampleInterval)
	defer ticker.Stop()
	for {
		recordDBPoolStats(registry, sqlDB.Stats())
		select {
		case <-done:
			return
		case <-ticker.C:
		}
	}
}
