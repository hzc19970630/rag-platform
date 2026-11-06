package conversation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
)

// sessions.go owns this slice of the service. Behavior and external contracts are
// unchanged; the split only narrows each file's responsibility.

func (s *Service) CreateConversation(ctx context.Context, principal authz.Principal, input CreateConversationInput) (models.Conversation, error) {
	if strings.TrimSpace(input.KnowledgeBaseID) == "" {
		return models.Conversation{}, apperror.Invalidf("knowledge_base_id is required")
	}
	if _, err := s.authorizedKnowledgeBase(ctx, principal, input.KnowledgeBaseID); err != nil {
		return models.Conversation{}, err
	}
	id, err := newID("conv")
	if err != nil {
		return models.Conversation{}, err
	}
	conversation := models.Conversation{ID: id, TenantID: principal.TenantID, KnowledgeBaseID: input.KnowledgeBaseID, UserID: principal.SubjectID, Title: truncateRunes(input.Title, conversationTitleMaxRunes)}
	if err := s.sessions.Create(ctx, conversation); err != nil {
		return models.Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return conversation, nil
}

func (s *Service) ListConversations(ctx context.Context, principal authz.Principal) ([]models.Conversation, error) {
	scope, err := s.authz.BuildScope(ctx, principal, authz.QueryTarget{})
	if err != nil {
		return nil, err
	}
	if len(scope.KnowledgeBaseIDs) == 0 {
		return []models.Conversation{}, nil
	}
	rows, err := s.sessions.List(ctx, principal.TenantID, principal.SubjectID, scope.KnowledgeBaseIDs)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *Service) ListMessages(ctx context.Context, principal authz.Principal, conversationID string) ([]MessageView, error) {
	conversation, err := s.loadConversation(ctx, principal, conversationID)
	if err != nil {
		return nil, err
	}
	messages, err := s.sessions.Messages(ctx, principal.TenantID, conversation.ID)
	if err != nil {
		return nil, err
	}
	views := make([]MessageView, 0, len(messages))
	for _, message := range messages {
		citations, err := s.loadCitations(ctx, principal.TenantID, message.ID)
		if err != nil {
			return nil, err
		}
		views = append(views, MessageView{Message: message, Citations: citations})
	}
	return views, nil
}

// ReplayEvents returns persisted stream events after the given sequence so a
// client that dropped the connection can resume without re-running the answer.

func (s *Service) loadConversation(ctx context.Context, principal authz.Principal, conversationID string) (models.Conversation, error) {
	if strings.TrimSpace(conversationID) == "" {
		return models.Conversation{}, apperror.ErrNotFound
	}
	return s.sessions.Load(ctx, principal.TenantID, principal.SubjectID, conversationID)
}

func (s *Service) authorizedKnowledgeBase(ctx context.Context, principal authz.Principal, knowledgeBaseID string) (authz.AuthorizationScope, error) {
	scope, err := s.authz.BuildScope(ctx, principal, authz.QueryTarget{KnowledgeBaseID: knowledgeBaseID})
	if err != nil {
		return authz.AuthorizationScope{}, err
	}
	allowed, err := s.authz.AuthorizeKnowledgeBase(ctx, principal, knowledgeBaseID, "read")
	if err != nil {
		return authz.AuthorizationScope{}, err
	}
	if !allowed {
		return authz.AuthorizationScope{}, apperror.ErrForbidden
	}
	return scope, nil
}

func (s *Service) touchConversation(ctx context.Context, conversation models.Conversation, query string) error {
	values := map[string]any{"updated_at": time.Now()}
	if strings.TrimSpace(conversation.Title) == "" {
		values["title"] = s.titleFor(ctx, query)
	}
	return s.sessions.Touch(ctx, conversation.TenantID, conversation.ID, values)
}
