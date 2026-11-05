package conversation

import (
	"context"

	"github.com/example/rag-platform/internal/contracts/model"
)

// Chatter is the generation port owned by this package. Adapters implement it
// structurally; swapping the offline extractive adapter for a hosted model is a
// configuration change, not a domain change.
type Chatter interface {
	Complete(ctx context.Context, request model.ChatRequest) (model.ChatResponse, error)
	Name() string
}
