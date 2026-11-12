// Package conversation owns sessions, messages, citation registration and the
// streaming event log. Online answers run inside the API request lifecycle and
// are persisted here, not in Redis.
package conversation

import (
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
	"github.com/hzc19970630/rag-platform/internal/status"
)

// Online answer state machine (design 7.5). The terminal states are the ones
// that describe how the answer was delivered.
const (
	AnswerReceived           = status.AnswerReceived
	AnswerAuthorized         = status.AnswerAuthorized
	AnswerPlanned            = status.AnswerPlanned
	AnswerRetrieving         = status.AnswerRetrieving
	AnswerGrounded           = status.AnswerGrounded
	AnswerGenerating         = status.AnswerGenerating
	AnswerCitationValidating = status.AnswerCitationValidating
	AnswerDelivering         = status.AnswerDelivering
	AnswerCompleted          = status.AnswerCompleted
	AnswerRefused            = status.AnswerRefused
	AnswerFailed             = status.Failed
	AnswerTimedOut           = status.AnswerTimedOut
	AnswerCancelled          = status.Cancelled
)

const (
	MessageRoleUser      = "user"
	MessageRoleAssistant = "assistant"

	MessageStatusCompleted = status.AnswerCompleted
	MessageStatusFailed    = status.Failed
	MessageStatusCancelled = status.Cancelled

	// Event types emitted over SSE. Each event carries event_id, request_id and
	// sequence so a client can resume from the last id it saw.
	EventStatus     = "status"
	EventReferences = "references"
	EventThinking   = "thinking"
	EventAnswer     = "answer"
	EventError      = "error"
	EventComplete   = "complete"
)

// Error codes the client can branch on.
const (
	CodeInsufficientEvidence = "insufficient_evidence"
	CodeCitationRejected     = "citation_rejected"
	CodeUngroundedAnswer     = "ungrounded_answer"
	CodeModelError           = "model_error"
	CodeTimeout              = "timeout"
	CodeClientDisconnected   = "client_disconnected"
	CodeCancelled            = "cancelled"
	// CodeWorkerLost marks a job whose worker lease expired; the answer will
	// never complete and the stream receives a terminal error.
	CodeWorkerLost = "worker_lost"
	// CodeWorkerError marks a retryable worker failure; the job is requeued.
	CodeWorkerError = "worker_error"
	// CodeQueueUnavailable means the question was accepted but never enqueued.
	CodeQueueUnavailable = "queue_unavailable"
	// CodeJobInvalid means the durable job could not be reconstructed.
	CodeJobInvalid = "job_invalid"
	// CodeForbidden marks a job whose scope no longer authorizes the question.
	CodeForbidden = "forbidden"
)

// Event is one stream message. Data becomes the JSON payload.
type Event struct {
	EventID   string         `json:"event_id"`
	RequestID string         `json:"request_id"`
	Sequence  int            `json:"sequence"`
	Type      string         `json:"type"`
	Data      map[string]any `json:"data,omitempty"`
}

// Emitter writes an event to the transport. Returning an error stops the run:
// a failed write usually means the client disconnected.
type Emitter func(Event) error

// AskInput is one question. ConversationID selects the session, which fixes the
// knowledge base and its permission boundary.
type AskInput struct {
	ConversationID string
	Query          string
	RequestID      string
	// SessionOverride is the third (request) tier of retrieval parameters.
	SessionOverride string
}

// CitationView is an expanded, validated citation returned to the client.
type CitationView struct {
	Index             int     `json:"index"`
	ChunkID           string  `json:"chunkId"`
	DocumentID        string  `json:"documentId"`
	DocumentVersionID string  `json:"documentVersionId"`
	KnowledgeBaseID   string  `json:"knowledgeBaseId"`
	HeadingPath       string  `json:"headingPath"`
	PageNumber        int     `json:"pageNumber"`
	SourcePath        string  `json:"sourcePath"`
	Quote             string  `json:"quote"`
	Rank              int     `json:"rank"`
	Score             float64 `json:"score"`
}

// Validation captures the citation-validation outcome recorded on the message.
type Validation struct {
	Valid              bool     `json:"valid"`
	Reason             string   `json:"reason,omitempty"`
	CitedIndexes       []int    `json:"citedIndexes"`
	InvalidIndexes     []int    `json:"invalidIndexes,omitempty"`
	RegisteredIndexes  int      `json:"registeredIndexes"`
	CitedDocuments     []string `json:"citedDocuments"`
	EvidenceDocuments  []string `json:"evidenceDocuments"`
	Ungrounded         bool     `json:"ungrounded"`
	ConflictSuspected  bool     `json:"conflictSuspected"`
	InjectionSuspected bool     `json:"injectionSuspected"`
}

// AnswerResult is the recorded outcome of one question.
type AnswerResult struct {
	MessageID        string                `json:"messageId"`
	RequestID        string                `json:"requestId"`
	Status           string                `json:"status"`
	Content          string                `json:"content"`
	Citations        []CitationView        `json:"citations"`
	Validation       Validation            `json:"citationValidation"`
	EvidenceSet      retrieval.EvidenceSet `json:"evidenceSet"`
	Model            string                `json:"model"`
	LatencyMS        int64                 `json:"latencyMs"`
	PromptTokens     int                   `json:"promptTokens"`
	CompletionTokens int                   `json:"completionTokens"`
	ErrorCode        string                `json:"errorCode,omitempty"`
	ErrorMessage     string                `json:"errorMessage,omitempty"`
}

// MessageView is a stored message with its citations, used by the session
// history endpoint.
type MessageView struct {
	Message   models.Message `json:"message"`
	Citations []CitationView `json:"citations"`
}

// TokenUsage is persisted alongside the answer for cost and latency reporting.
type TokenUsage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
}

// conversationTitleMaxRunes bounds the deterministic fallback title.
const conversationTitleMaxRunes = 60
