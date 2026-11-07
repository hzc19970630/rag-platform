package app

import (
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/conversation"
	"github.com/hzc19970630/rag-platform/internal/platform/llm"
)

// NewChatter resolves the generation adapter from configuration. The
// conversation layer depends only on its own Chatter port, so swapping the
// offline extractive adapter for a hosted model is a configuration change.
func NewChatter(cfg config.Config) (conversation.Chatter, error) {
	switch cfg.Chat.Source {
	case "local":
		return llm.NewLocalChatter(), nil
	case "remote":
		return llm.NewRemoteChatter(llm.RemoteChatterOptions{
			BaseURL:   cfg.Chat.BaseURL,
			APIKey:    cfg.Chat.APIKey,
			Model:     cfg.Chat.Model,
			MaxTokens: cfg.Chat.MaxTokens,
			Timeout:   time.Duration(cfg.Chat.TimeoutSec) * time.Second,
			HealthURL: cfg.Chat.HealthURL,
		})
	default:
		return nil, fmt.Errorf("unsupported CHAT_SOURCE %q", cfg.Chat.Source)
	}
}
