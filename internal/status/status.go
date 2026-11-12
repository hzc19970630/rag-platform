// Package status is the single source of lifecycle values used by SQL recall,
// workers, projections, queues and operator views. Domain packages alias these
// values instead of declaring their own copies, so a rename cannot silently
// miss one query.
package status

const (
	// Document, version and ingestion pipeline states.
	Received      = "RECEIVED"
	Stored        = "STORED"
	Parsing       = "PARSING"
	Parsed        = "PARSED"
	Normalizing   = "NORMALIZING"
	Normalized    = "NORMALIZED"
	Chunking      = "CHUNKING"
	Chunked       = "CHUNKED"
	Embedding     = "EMBEDDING"
	Embedded      = "EMBEDDED"
	Indexing      = "INDEXING"
	EvidenceReady = "EVIDENCE_READY"
	Parent        = "PARENT"
	Stale         = "STALE"
	Failed        = "FAILED"
	Cancelled     = "CANCELLED"

	// Stage, inbox, outbox and dead-letter states.
	Pending    = "PENDING"
	Processing = "PROCESSING"
	Running    = "RUNNING"
	Succeeded  = "SUCCEEDED"
	Retrying   = "RETRYING"
	Resolved   = "RESOLVED"
	Discarded  = "DISCARDED"

	// Wiki and relation projection run states.
	WikiPending        = "WIKI_PENDING"
	WikiGenerating     = "WIKI_GENERATING"
	WikiReady          = "WIKI_READY"
	WikiFailed         = "FAILED_WIKI"
	WikiRetrying       = "RETRY_WIKI"
	RelationPending    = "RELATION_PENDING"
	RelationExtracting = "RELATION_EXTRACTING"
	RelationReady      = "RELATION_READY"
	RelationFailed     = "FAILED_RELATION"
	RelationRetrying   = "RETRY_RELATION"

	// Review, wiki page and knowledge index states.
	ReviewApproved = "APPROVED"
	ReviewRejected = "REJECTED"
	Draft          = "DRAFT"
	Published      = "PUBLISHED"
	ReviewRequired = "REVIEW_REQUIRED"
	Archived       = "ARCHIVED"
	Active         = "ACTIVE"

	// Graph snapshot states.
	Building      = "BUILDING"
	PendingReview = "PENDING_REVIEW"
	Superseded    = "SUPERSEDED"

	// Connector sync run states.
	SyncPartial      = "PARTIAL"
	Tombstoned       = "TOMBSTONED"
	KnowledgeReady   = "KNOWLEDGE_READY"
	KnowledgePartial = "KNOWLEDGE_PARTIAL"

	// Durable answer job states.
	AnswerQueued    = "QUEUED"
	AnswerRunning   = "RUNNING"
	AnswerCompleted = "COMPLETED"

	// Online answer lifecycle.
	AnswerReceived           = "RECEIVED"
	AnswerAuthorized         = "AUTHORIZED"
	AnswerPlanned            = "PLANNED"
	AnswerRetrieving         = "RETRIEVING"
	AnswerGrounded           = "GROUNDED"
	AnswerGenerating         = "GENERATING"
	AnswerCitationValidating = "CITATION_VALIDATING"
	AnswerDelivering         = "DELIVERING"
	AnswerRefused            = "REFUSED"
	AnswerTimedOut           = "TIMEOUT"
)

// Mechanisms are the independent retry/lifecycle machinery an operator has to
// reason about when diagnosing one task.
const (
	MechanismAsynqRetry      = "asynq_retry"
	MechanismInboxLease      = "inbox_lease"
	MechanismOutboxAttempts  = "outbox_attempts"
	MechanismStageAttempts   = "stage_attempts"
	MechanismReconcile       = "reconcile"
	MechanismStaleExpiry     = "stale_expiry"
	MechanismProjectionRetry = "projection_retry"
	MechanismSyncRetry       = "sync_retry"
	MechanismAnswerLease     = "answer_lease"
)

// Transition is one edge in the cross-mechanism state table. The table is
// documentation and a test fixture; it does not replace the enforcers, which
// remain in each domain.
type Transition struct {
	Mechanism string
	From      string
	To        string
	Trigger   string
}

// Transitions covers the eight mechanisms named by the D2 milestone, plus the
// durable answer lease added by M13 async answers.
var Transitions = []Transition{
	{MechanismOutboxAttempts, "", Pending, "domain transaction writes an outbox event"},
	{MechanismOutboxAttempts, Pending, Published, "outbox publisher enqueues the task"},
	{MechanismOutboxAttempts, Pending, Pending, "publish fails; attempts and next_attempt_at advance"},
	{MechanismOutboxAttempts, Pending, Failed, "attempts reach OUTBOX_MAX_ATTEMPTS"},

	{MechanismInboxLease, "", Processing, "worker claims the idempotency key"},
	{MechanismInboxLease, Processing, Processing, "worker heartbeat extends lease_until"},
	{MechanismInboxLease, Processing, Succeeded, "stage completes"},
	{MechanismInboxLease, Processing, Failed, "task fails terminally or retry budget is spent"},
	{MechanismInboxLease, Processing, Processing, "reconcile takes over an expired lease"},

	{MechanismStageAttempts, Pending, Processing, "worker starts the stage"},
	{MechanismStageAttempts, Processing, Succeeded, "stage work succeeds"},
	{MechanismStageAttempts, Processing, Pending, "reconcile requeues a stale stage"},
	{MechanismStageAttempts, Processing, Failed, "stage work fails"},
	{MechanismStageAttempts, Processing, Cancelled, "document or pipeline is cancelled"},

	{MechanismAsynqRetry, "", Pending, "task enqueued"},
	{MechanismAsynqRetry, Pending, Processing, "worker delivers the task"},
	{MechanismAsynqRetry, Processing, Retrying, "handler returns a retryable error"},
	{MechanismAsynqRetry, Retrying, Processing, "Asynq redelivers before MaxRetry"},
	{MechanismAsynqRetry, Retrying, Archived, "MaxRetry is exhausted; queue archives the task"},

	{MechanismReconcile, Processing, Pending, "stage heartbeat is stale and worth retrying"},
	{MechanismReconcile, Processing, Failed, "stage retry budget is spent"},
	{MechanismReconcile, Pending, Failed, "inbox lease expired and attempts are exhausted"},

	{MechanismStaleExpiry, Pending, Failed, "connector or answer lease expired"},
	{MechanismStaleExpiry, Processing, Failed, "worker disappeared before the lease was renewed"},

	{MechanismProjectionRetry, Pending, Processing, "projection stage starts"},
	{MechanismProjectionRetry, Processing, Succeeded, "projection stage succeeds"},
	{MechanismProjectionRetry, Processing, Failed, "projection stage fails"},
	{MechanismProjectionRetry, Failed, Pending, "operator retries with a new attempt id"},
	{MechanismProjectionRetry, Processing, Stale, "ACL or source hash drift invalidates the derived output"},

	{MechanismSyncRetry, Pending, Running, "sync worker starts the run"},
	{MechanismSyncRetry, Running, Succeeded, "all items synced"},
	{MechanismSyncRetry, Running, SyncPartial, "some items failed but the run completed"},
	{MechanismSyncRetry, Running, Failed, "run failed after retries"},
	{MechanismSyncRetry, Running, Cancelled, "operator cancels the run"},

	{MechanismAnswerLease, AnswerQueued, AnswerRunning, "worker claims the answer job"},
	{MechanismAnswerLease, AnswerRunning, AnswerCompleted, "terminal answer message is recorded"},
	{MechanismAnswerLease, AnswerRunning, Failed, "worker reports a terminal error"},
	{MechanismAnswerLease, AnswerQueued, Failed, "stale-answer sweep expires the job"},
	{MechanismAnswerLease, AnswerRunning, Failed, "lease expired before the worker renewed it"},
}
