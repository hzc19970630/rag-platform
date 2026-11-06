package conversation

import "gorm.io/gorm"

// Stores bundles the persistence ports the conversation service needs. Tests
// and alternate storage backends can fill any field; the service never sees a
// *gorm.DB.
type Stores struct {
	Sessions SessionStore
	Events   EventStore
	Messages MessageStore
	Jobs     AnswerJobStore
}

// NewPostgresStores wires the Postgres adapters behind every port.
func NewPostgresStores(db *gorm.DB) Stores {
	return Stores{
		Sessions: NewPostgresSessionStore(db),
		Events:   NewPostgresEventStore(db),
		Messages: NewPostgresMessageStore(db),
		Jobs:     NewPostgresAnswerJobStore(db),
	}
}
