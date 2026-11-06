package conversation

import "github.com/example/rag-platform/internal/contracts/model"

// Chatter is the shared generation port. The alias keeps this package's public
// name while the method set lives in contracts/model.
type Chatter = model.Chatter
