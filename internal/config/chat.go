package config

import (
	"fmt"
	"net/url"
	"strings"
)

// ChatConfig describes the generation model. Like the embedding model it is
// configuration, not code, and it uses the same local/remote split: the local
// adapter is a deterministic, offline stand-in for development and acceptance.
type ChatConfig struct {
	Source     string
	BaseURL    string
	APIKey     string
	Model      string
	TimeoutSec int
	MaxTokens  int
	// HealthURL is the provider liveness endpoint used by the model catalog.
	// Empty derives it from BaseURL (/models); a non-empty value must be a URL.
	HealthURL string
	// Models lists additional routable chat models as name[@version]. The
	// deployment default (LLM_CHAT_MODEL) is always registered. Empty keeps a
	// single-model deployment.
	Models []string
	// HistoryTurns is how many completed user/assistant turns are replayed to
	// the model. Zero disables multi-turn context.
	HistoryTurns int
	// HistoryTokenLimit bounds the replayed history independently of the
	// evidence context budget.
	HistoryTokenLimit int
	// QueryRewriteEnabled enables the retrieval-side follow-up rewriter. It is a
	// separate model call only when history exists and the chat adapter is real.
	QueryRewriteEnabled bool
	// QueryRewriteTimeoutSec bounds one rewrite call.
	QueryRewriteTimeoutSec int
}

// AnswerConfig controls the async answer path: the API enqueues a job and tails
// the persisted stream events instead of generating inside the HTTP request.
type AnswerConfig struct {
	// AsyncEnabled routes POST /sessions/:id/messages through the worker queue.
	// False keeps the synchronous path as the rollback switch.
	AsyncEnabled bool
	// TailTimeoutSec bounds how long one HTTP request tails a job before it
	// gives up and relies on replay after reconnect.
	TailTimeoutSec int
	// PollIntervalMS is the replay polling interval for an open tail.
	PollIntervalMS int
	// LeaseSeconds is how long a claimed job is reserved before the maintenance
	// sweep treats the worker as lost.
	LeaseSeconds int
}

func (c AnswerConfig) Validate() error {
	if !c.AsyncEnabled {
		return nil
	}
	if c.TailTimeoutSec <= 0 {
		return fmt.Errorf("ANSWER_TAIL_TIMEOUT_SECONDS must be a positive integer")
	}
	if c.PollIntervalMS <= 0 {
		return fmt.Errorf("ANSWER_ASYNC_POLL_INTERVAL_MS must be a positive integer")
	}
	if c.LeaseSeconds <= 0 {
		return fmt.Errorf("ANSWER_ASYNC_LEASE_SECONDS must be a positive integer")
	}
	return nil
}

func (c ChatConfig) Validate(appEnv string) error {
	if err := validateModelList("CHAT_MODELS", c.Models); err != nil {
		return err
	}
	if c.HistoryTurns < 0 {
		return fmt.Errorf("CHAT_HISTORY_TURNS must be zero or positive")
	}
	if c.HistoryTokenLimit < 0 {
		return fmt.Errorf("CHAT_HISTORY_TOKEN_LIMIT must be zero or positive")
	}
	if c.QueryRewriteTimeoutSec < 0 {
		return fmt.Errorf("CHAT_QUERY_REWRITE_TIMEOUT_SECONDS must be zero or positive")
	}
	switch c.Source {
	case "local":
		// Offline deterministic adapter, development and acceptance only.
		if appEnv == "production" {
			return fmt.Errorf("CHAT_SOURCE=local is a development adapter and must not run in production")
		}
	case "remote":
		if strings.TrimSpace(c.BaseURL) == "" {
			return fmt.Errorf("CHAT_BASE_URL is required when CHAT_SOURCE=remote")
		}
		if strings.TrimSpace(c.Model) == "" {
			return fmt.Errorf("LLM_CHAT_MODEL is required when CHAT_SOURCE=remote")
		}
		parsed, err := url.Parse(c.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("CHAT_BASE_URL must be a valid base URL")
		}
		if strings.TrimSpace(c.HealthURL) != "" {
			health, err := url.Parse(c.HealthURL)
			if err != nil || health.Scheme == "" || health.Host == "" {
				return fmt.Errorf("CHAT_HEALTH_URL must be a valid URL")
			}
		}
	default:
		return fmt.Errorf("CHAT_SOURCE must be local or remote, got %q", c.Source)
	}
	return nil
}

// RerankConfig describes the reranking adapter. Rerank is a configurable
// capability, not a mandatory dependency, so the default source is none
// (pass-through): turning rerank on has to be an explicit, evaluated decision.
// lexical keeps the offline deterministic reranker reachable for evaluation;
// remote calls an OpenAI-compatible /rerank endpoint.
type RerankConfig struct {
	Source     string
	BaseURL    string
	APIKey     string
	Model      string
	TimeoutSec int
	// HealthURL is the provider liveness endpoint used by the model catalog.
	// Empty derives it from BaseURL (/health); a non-empty value must be a URL.
	HealthURL string
	// Models lists additional routable rerank models as name[@version].
	Models []string
}

func (r RerankConfig) Validate() error {
	if err := validateModelList("RERANK_MODELS", r.Models); err != nil {
		return err
	}
	switch r.Source {
	case "none", "lexical":
		// Offline adapters: pass-through and overlap scoring. No endpoint needed.
	case "remote":
		if strings.TrimSpace(r.BaseURL) == "" {
			return fmt.Errorf("RERANK_BASE_URL is required when RERANK_SOURCE=remote")
		}
		if strings.TrimSpace(r.Model) == "" {
			return fmt.Errorf("LLM_RERANK_MODEL is required when RERANK_SOURCE=remote")
		}
		parsed, err := url.Parse(r.BaseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("RERANK_BASE_URL must be a valid base URL")
		}
		if strings.TrimSpace(r.HealthURL) != "" {
			health, err := url.Parse(r.HealthURL)
			if err != nil || health.Scheme == "" || health.Host == "" {
				return fmt.Errorf("RERANK_HEALTH_URL must be a valid URL")
			}
		}
	default:
		return fmt.Errorf("RERANK_SOURCE must be none, lexical or remote, got %q", r.Source)
	}
	return nil
}
