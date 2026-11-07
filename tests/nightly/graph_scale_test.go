//go:build nightly

// Package nightly_test holds scale and load checks that are too slow for every
// pull request. The nightly workflow runs them with -race.
package nightly_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/relationstore"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The relation store loads a snapshot before the bounded BFS runs. A 5k/20k
// snapshot documents the memory/time envelope that D6 (graph paging) must
// improve without regressing m7.
func TestGraphLoadsLargeSnapshotWithinBudget(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.GraphSnapshot{}, &models.GraphEntity{}, &models.GraphEntityAlias{},
		&models.GraphRelation{}, &models.GraphRelationSource{},
	); err != nil {
		t.Fatalf("migrate graph: %v", err)
	}

	const entityCount = 5000
	const relationCount = 20000
	now := time.Now().UTC()
	snapshot := models.GraphSnapshot{
		ID: "snap-scale", TenantID: "tenant-scale", KnowledgeBaseID: "kb-scale",
		ProjectionRunID: "run-scale", Version: 1, Status: models.GraphSnapshotActive,
		ACLVersion: 1, Stats: "{}", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	entities := make([]models.GraphEntity, 0, entityCount)
	for index := 0; index < entityCount; index++ {
		entities = append(entities, models.GraphEntity{
			ID: fmt.Sprintf("entity-%05d", index), TenantID: "tenant-scale", KnowledgeBaseID: "kb-scale",
			SnapshotID: snapshot.ID, CanonicalKey: fmt.Sprintf("key-%d", index), Name: fmt.Sprintf("Entity %d", index),
			Properties: "{}", ACLVersion: 1, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now,
		})
	}
	if err := db.CreateInBatches(entities, 500).Error; err != nil {
		t.Fatalf("seed entities: %v", err)
	}
	relations := make([]models.GraphRelation, 0, relationCount)
	for index := 0; index < relationCount; index++ {
		relations = append(relations, models.GraphRelation{
			ID: fmt.Sprintf("relation-%05d", index), TenantID: "tenant-scale", KnowledgeBaseID: "kb-scale",
			SnapshotID: snapshot.ID, SubjectID: fmt.Sprintf("entity-%05d", index%entityCount),
			Predicate: "RELATED_TO", ObjectID: fmt.Sprintf("entity-%05d", (index*7+1)%entityCount),
			Properties: "{}", Confidence: 1, ACLVersion: 1, Status: "ACTIVE", CreatedAt: now, UpdatedAt: now,
		})
	}
	if err := db.CreateInBatches(relations, 500).Error; err != nil {
		t.Fatalf("seed relations: %v", err)
	}

	store := relationstore.New(db)
	started := time.Now()
	graph, err := store.Graph(context.Background(), snapshot.ID)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("load graph: %v", err)
	}
	if len(graph.Entities) != entityCount || len(graph.Relations) != relationCount {
		t.Fatalf("graph size = %d entities / %d relations", len(graph.Entities), len(graph.Relations))
	}
	if elapsed > 30*time.Second {
		t.Fatalf("graph load took %s for %d entities / %d relations", elapsed, entityCount, relationCount)
	}
	t.Logf("graph load: %d entities, %d relations in %s", entityCount, relationCount, elapsed)
}
