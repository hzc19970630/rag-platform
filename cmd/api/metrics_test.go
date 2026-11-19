package main

import (
	"database/sql"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/observability"
)

func TestRecordDBPoolStatsPublishesOccupancy(t *testing.T) {
	registry := observability.New()
	recordDBPoolStats(registry, sql.DBStats{
		InUse:              7,
		MaxOpenConnections: 20,
		WaitCount:          3,
		WaitDuration:       1500 * time.Millisecond,
	})

	gauges := registry.Snapshot()["gauges"].(map[string]float64)
	want := map[string]float64{
		"db_pool_in_use":       7,
		"db_pool_max_open":     20,
		"db_pool_wait_count":   3,
		"db_pool_wait_seconds": 1.5,
	}
	for name, value := range want {
		if got := gauges[name]; got != value {
			t.Fatalf("%s = %v, want %v", name, got, value)
		}
	}
}

func TestRecordDBPoolStatsWithoutRegistryIsNoop(t *testing.T) {
	recordDBPoolStats(nil, sql.DBStats{InUse: 1})
}
