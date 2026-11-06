package conversation

import (
	"context"
	"errors"
	"fmt"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

// SessionStore is the narrow persistence port for the session/message
// aggregate. The service no longer needs a *gorm.DB to create or read a
// conversation; another store can implement the same methods.
type SessionStore interface {
	Create(ctx context.Context, conversation models.Conversation) error
	List(ctx context.Context, tenantID, userID string, knowledgeBaseIDs []string) ([]models.Conversation, error)
	Load(ctx context.Context, tenantID, userID, conversationID string) (models.Conversation, error)
	Messages(ctx context.Context, tenantID, conversationID string) ([]models.Message, error)
	Touch(ctx context.Context, tenantID, conversationID string, values map[string]any) error
}

type PostgresSessionStore struct {
	db *gorm.DB
}

func NewPostgresSessionStore(db *gorm.DB) *PostgresSessionStore {
	return &PostgresSessionStore{db: db}
}

func (s *PostgresSessionStore) Create(ctx context.Context, conversation models.Conversation) error {
	return s.db.WithContext(ctx).Create(&conversation).Error
}

func (s *PostgresSessionStore) List(ctx context.Context, tenantID, userID string, knowledgeBaseIDs []string) ([]models.Conversation, error) {
	var rows []models.Conversation
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Where("user_id = ? AND knowledge_base_id IN ?", userID, knowledgeBaseIDs).
		Order("updated_at DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	return rows, nil
}

func (s *PostgresSessionStore) Load(ctx context.Context, tenantID, userID, conversationID string) (models.Conversation, error) {
	var conversation models.Conversation
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Where("user_id = ? AND id = ?", userID, conversationID).First(&conversation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Conversation{}, apperror.ErrNotFound
		}
		return models.Conversation{}, fmt.Errorf("load conversation: %w", err)
	}
	return conversation, nil
}

func (s *PostgresSessionStore) Messages(ctx context.Context, tenantID, conversationID string) ([]models.Message, error) {
	var messages []models.Message
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Where("conversation_id = ?", conversationID).Order("created_at, id").Find(&messages).Error; err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	return messages, nil
}

func (s *PostgresSessionStore) Touch(ctx context.Context, tenantID, conversationID string, values map[string]any) error {
	return tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Model(&models.Conversation{}).Where("id = ?", conversationID).Updates(values).Error
}
