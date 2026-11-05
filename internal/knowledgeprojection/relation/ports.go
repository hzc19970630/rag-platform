package relation

import (
	"context"

	"github.com/example/rag-platform/internal/contracts/model"
)

// Chatter is the generation port owned by this package. Adapters implement it
// structurally, so the projection pipeline does not depend on a provider.
type Chatter interface {
	Complete(ctx context.Context, request model.ChatRequest) (model.ChatResponse, error)
	Name() string
}
