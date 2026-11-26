package conversation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/contracts/tasks"
	"github.com/hzc19970630/rag-platform/internal/agenttools"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantquota"
	"github.com/hzc19970630/rag-platform/internal/prompts"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
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
	sessions      SessionStore
	events        EventStore
	messages      MessageStore
	jobs          AnswerJobStore
	authz         *authz.Service
	retrieval     *retrieval.Service
	chat          Chatter
	models        ChatModelResolver
	metrics       metrics.Recorder
	prompts       *prompts.Registry
	timeout       time.Duration
	dailyCap      int
	quotas        QuotaResolver
	historyTurn   int
	historyMax    int
	answers       AnswerPublisher
	async         bool
	leaseTTL      time.Duration
	tools         ToolRuntime
	tableFallback TableFallback
	toolRounds    int
	toolCalls     int
	toolTimeout   time.Duration
}

// TableFallback answers one table question deterministically when the bound
// model cannot call tools. Evidence is injected into the normal generation.
type TableFallback interface {
	Answer(ctx context.Context, principal authz.Principal, knowledgeBaseID, question string) (string, []agenttools.Evidence, bool)
}

// ToolRuntime owns the KB-level tool policy and the shared registry. It is nil
// in deployments that never enable tools.
type ToolRuntime interface {
	Enabled(ctx context.Context, scope authz.AuthorizationScope, knowledgeBaseID string) bool
	Registry() *agenttools.Registry
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

// QuotaResolver reads the tenant's current limits. When set, it supersedes the
// deployment-level ChatRequestsPerDay default captured at construction.
type QuotaResolver interface {
	Limits(ctx context.Context, tenantID string) (tenantquota.Limits, error)
}

type Options struct {
	// Stores are the narrow persistence ports. The service holds no *gorm.DB,
	// so an alternate backend can provide its own implementation.
	Stores    Stores
	Authz     *authz.Service
	Retrieval *retrieval.Service
	Chat      Chatter
	Models    ChatModelResolver
	Metrics   metrics.Recorder
	Prompts   *prompts.Registry
	Timeout   time.Duration
	// ChatRequestsPerDay is the deployment-level daily question budget. Zero
	// disables the limit. QuotaResolver, when set, supplies the per-tenant value.
	ChatRequestsPerDay int
	// QuotaResolver supplies the tenant's effective limits, including overrides
	// stored in tenants.quota.
	QuotaResolver QuotaResolver
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
	// ToolRuntime enables the built-in tool layer. Nil keeps the legacy
	// single-pass RAG path.
	ToolRuntime ToolRuntime
	// TableFallback keeps table answers available when the model has no
	// function calling. Nil disables the fallback.
	TableFallback TableFallback
	// ToolMaxRounds bounds model/tool alternations; ToolMaxCalls bounds the
	// tool invocations across the whole answer.
	ToolMaxRounds int
	ToolMaxCalls  int
	// ToolTimeout bounds one tool invocation.
	ToolTimeout time.Duration
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
	toolRounds := options.ToolMaxRounds
	if toolRounds <= 0 {
		toolRounds = 3
	}
	toolCalls := options.ToolMaxCalls
	if toolCalls <= 0 {
		toolCalls = 8
	}
	toolTimeout := options.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = 20 * time.Second
	}
	return &Service{
		sessions:      options.Stores.Sessions,
		events:        options.Stores.Events,
		messages:      options.Stores.Messages,
		jobs:          options.Stores.Jobs,
		authz:         options.Authz,
		retrieval:     options.Retrieval,
		chat:          options.Chat,
		models:        options.Models,
		metrics:       recorder,
		prompts:       options.Prompts,
		timeout:       timeout,
		dailyCap:      options.ChatRequestsPerDay,
		quotas:        options.QuotaResolver,
		historyTurn:   options.HistoryTurns,
		historyMax:    options.HistoryTokenLimit,
		answers:       options.AnswerPublisher,
		async:         options.AsyncEnabled,
		leaseTTL:      lease,
		tools:         options.ToolRuntime,
		tableFallback: options.TableFallback,
		toolRounds:    toolRounds,
		toolCalls:     toolCalls,
		toolTimeout:   toolTimeout,
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
