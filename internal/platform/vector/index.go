package vector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"gorm.io/gorm"
)

// ChunksIndexName is the single HNSW index that serves chunk vector search. It
// covers exactly one vector dimension at a time, so the dimension is a
// deployment contract rather than a tunable setting.
const ChunksIndexName = "chunks_vector_idx"

var halfvecDimensionPattern = regexp.MustCompile(`halfvec\((\d+)\)`)

// ChunksIndexDimension reports the vector dimension of the chunks HNSW index.
// The boolean is false when the index does not exist yet.
func ChunksIndexDimension(ctx context.Context, db *gorm.DB) (int, bool, error) {
	row := db.WithContext(ctx).Raw("SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?", ChunksIndexName).Row()
	var definition string
	if err := row.Scan(&definition); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read chunks vector index definition: %w", err)
	}
	dimension, err := dimensionFromIndexDef(definition)
	if err != nil {
		return 0, false, err
	}
	return dimension, true, nil
}

// CreateHNSWIndex builds the chunks HNSW index for the given dimension. HNSW
// indexes cannot be re-dimensioned in place, so a mismatched index is dropped
// and rebuilt. It returns true when the index was (re)created.
func CreateHNSWIndex(ctx context.Context, db *gorm.DB, dimension int) (bool, error) {
	if dimension <= 0 {
		return false, fmt.Errorf("vector dimension must be a positive integer, got %d", dimension)
	}
	current, exists, err := ChunksIndexDimension(ctx, db)
	if err != nil {
		return false, err
	}
	if exists && current == dimension {
		return false, nil
	}
	// dimension is an int already validated positive, so it is safe to inline.
	createIndex := fmt.Sprintf(
		"CREATE INDEX %s ON chunks USING hnsw ((embedding::halfvec(%d)) halfvec_cosine_ops) WITH (m = 16, ef_construction = 64) WHERE embedding_dimension = %d",
		ChunksIndexName, dimension, dimension,
	)
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DROP INDEX IF EXISTS " + ChunksIndexName).Error; err != nil {
			return fmt.Errorf("drop previous chunks vector index: %w", err)
		}
		if err := tx.Exec(createIndex).Error; err != nil {
			return fmt.Errorf("create chunks vector index at dimension %d: %w", dimension, err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func dimensionFromIndexDef(definition string) (int, error) {
	match := halfvecDimensionPattern.FindStringSubmatch(definition)
	if match == nil {
		return 0, fmt.Errorf("cannot parse halfvec dimension from index definition %q", definition)
	}
	dimension, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, fmt.Errorf("parse halfvec dimension from index definition %q: %w", definition, err)
	}
	return dimension, nil
}
