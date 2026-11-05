package conversation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/contracts/tasks"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/contracts/metrics"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/prompts"
	"github.com/example/rag-platform/internal/retrieval"
	"gorm.io/gorm"
)

// localChatName identifies the offline generation adapter. It is used only to
// decide whether a model call is worth making for a conversation title.
const localChatName = "local-extractive"

// terminalPersistTimeout bounds the database writes that record a terminal
// answer and its stream events after the client is gone. The HTTP request
// context is already canceled in that case, so these writes must not inherit
// its cancellation; they still need a bound of their own.
const terminalPersistTimeout = 15 * time.Second

type Service struct {
	db          *gorm.DB
	authz       *authz.Service
	retrieval   *retrieval.Service
	chat        Chatter
	models      ChatModelResolver
	metrics     metrics.Recorder
	prompts     *prompts.Registry
	timeout     time.Duration
	dailyCap    int
	historyTurn int
	historyMax  int
	answers     AnswerPublisher
	async       bool
	leaseTTL    time.Duration
}

// ChatModelResolver resolves the knowledge-base chat binding to the model this
// request must use. The registry applies provider health, fallback and quota.
type ChatModelResolver interface {
	ChatModel(ctx context.Context, tenantID, knowledgeBaseID string) (string, error)
}

// AnswerPublisher enqueues one async answer job. The port keeps the domain
// independent of Asynq and lets tests assert the exact payload.
type AnswerPublisher interface {
	PublishAnswer(ctx context.Context, task tasks.Answer) error
}

type Options struct {
	DB        *gorm.DB
	Authz     *authz.Service
	Retrieval *retrieval.Service
	Chat      Chatter
	Models    ChatModelResolver
	Metrics   metrics.Recorder
	Prompts   *prompts.Registry
	Timeout   time.Duration
	// ChatRequestsPerDay is the tenant-level daily question budget. Zero
	// disables the limit.
	ChatRequestsPerDay int
	// HistoryTurns is the number of completed user/assistant turns replayed to
	// generation. Zero disables history.
	HistoryTurns int
	// HistoryTokenLimit bounds the replayed history.
	HistoryTokenLimit int
	// AnswerPublisher is required when AsyncEnabled is true.
	AnswerPublisher AnswerPublisher
	// AsyncEnabled moves generation to a worker. False keeps Ask synchronous.
	AsyncEnabled bool
	// AnswerLease bounds how long a claimed job stays reserved for one worker.
	AnswerLease time.Duration
}

func NewService(options Options) *Service {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	recorder := options.Metrics
	if recorder == nil {
		recorder = metrics.Nop{}
	}
	lease := options.AnswerLease
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	return &Service{
		db:          options.DB,
		authz:       options.Authz,
		retrieval:   options.Retrieval,
		chat:        options.Chat,
		models:      options.Models,
		metrics:     recorder,
		prompts:     options.Prompts,
		timeout:     timeout,
		dailyCap:    options.ChatRequestsPerDay,
		historyTurn: options.HistoryTurns,
		historyMax:  options.HistoryTokenLimit,
		answers:     options.AnswerPublisher,
		async:       options.AsyncEnabled,
		leaseTTL:    lease,
	}
}

type CreateConversationInput struct {
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Title           string `json:"title"`
}

type finishInput struct {
	conversation     models.Conversation
	principal        authz.Principal
	requestID        string
	scope            authz.AuthorizationScope
	query            string
	started          time.Time
	evidence         retrieval.EvidenceSet
	validation       Validation
	content          string
	status           string
	code             string
	model            string
	message          string
	citations        []CitationView
	promptTokens     int
	completionTokens int
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func stableID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + "-" + hex.EncodeToString(digest[:])[:32]
}

func newID(prefix string) (string, error) {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "-" + hex.EncodeToString(buffer), nil
}
