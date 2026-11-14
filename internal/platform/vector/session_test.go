package vector

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPostgresIndexSessionSurfacesConnectionErrors(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite handle: %v", err)
	}
	connection, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	session := &postgresIndexSession{connection: connection}
	_ = connection.Close()
	ctx := context.Background()
	if _, err := session.PgvectorVersion(ctx); err == nil {
		t.Fatal("PgvectorVersion must fail on a closed connection")
	}
	if _, err := session.ChunkIndexes(ctx); err == nil {
		t.Fatal("ChunkIndexes must fail on a closed connection")
	}
	if err := session.Configure(ctx, IndexOptions{}); err == nil {
		t.Fatal("Configure must fail on a closed connection")
	}
	if err := session.CreateIndex(ctx, "idx", 8); err == nil {
		t.Fatal("CreateIndex must fail on a closed connection")
	}
	if err := session.DropIndex(ctx, "idx"); err == nil {
		t.Fatal("DropIndex must fail on a closed connection")
	}
	if _, err := session.IndexSizeBytes(ctx, "idx"); err == nil {
		t.Fatal("IndexSizeBytes must fail on a closed connection")
	}
	if err := session.reset(ctx); err == nil {
		t.Fatal("reset must fail on a closed connection")
	}
}

func TestPostgresVariantSessionExecAndErrors(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite handle: %v", err)
	}
	connection, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	session := postgresVariantSession{connection: connection}
	ctx := context.Background()
	if err := session.Exec(ctx, "CREATE TABLE variant_probe (id INTEGER)"); err != nil {
		t.Fatalf("Exec on sqlite: %v", err)
	}
	_ = connection.Close()
	if _, err := session.DimensionHistogram(ctx); err == nil {
		t.Fatal("DimensionHistogram must fail on a closed connection")
	}
	if err := session.Exec(ctx, "SELECT 1"); err == nil {
		t.Fatal("Exec must fail on a closed connection")
	}
}

func TestPostgresInspectSessionSurfacesConnectionErrors(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite handle: %v", err)
	}
	connection, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	session := postgresInspectSession{connection: connection}
	_ = connection.Close()
	ctx := context.Background()
	if _, err := session.PgvectorVersion(ctx); err == nil {
		t.Fatal("PgvectorVersion must fail on a closed connection")
	}
	if _, err := session.ChunkIndexes(ctx); err == nil {
		t.Fatal("ChunkIndexes must fail on a closed connection")
	}
	if _, err := session.IndexSizeBytes(ctx, "idx"); err == nil {
		t.Fatal("IndexSizeBytes must fail on a closed connection")
	}
	if _, err := session.DatabaseSize(ctx); err == nil {
		t.Fatal("DatabaseSize must fail on a closed connection")
	}
	if _, err := session.ChunksHeapSize(ctx); err == nil {
		t.Fatal("ChunksHeapSize must fail on a closed connection")
	}
	if _, err := session.DimensionHistogram(ctx); err == nil {
		t.Fatal("DimensionHistogram must fail on a closed connection")
	}
}

func TestCheckChunksIndexesUsesDatabaseHandle(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := CheckChunksIndexes(context.Background(), db); err == nil {
		t.Fatal("CheckChunksIndexes must fail without the pgvector catalog")
	}
}

func TestDiskFreeBytesReadsRootFilesystem(t *testing.T) {
	free, err := diskFreeBytes("/")
	if err != nil {
		t.Fatalf("diskFreeBytes: %v", err)
	}
	if free == 0 {
		t.Fatal("diskFreeBytes returned zero")
	}
}
