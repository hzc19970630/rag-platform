// Package embedding is the single Embedder port shared by ingestion, retrieval
// and projections. A model capability change updates this interface once.
package embedding

import "context"

type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimension reports the vector length this adapter produces. Callers
	// validate it against stored rows so a model or dimension change fails
	// loudly instead of writing vectors that cannot be searched.
	Dimension() int
	// Name identifies the model so a knowledge base bound to a different one
	// cannot be written or queried with an incomparable vector.
	Name() string
}
