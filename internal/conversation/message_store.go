package conversation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"gorm.io/gorm"
)

// MessageStore is the narrow persistence port for messages and their
// citations. It covers the writes and reads an answer needs; the service keeps
// the sequencing and validation rules.
type MessageStore interface {
	Create(ctx context.Context, message models.Message) error
	CreateCitations(ctx context.Context, citations []models.MessageCitation) error
	Citations(ctx context.Context, tenantID, messageID string) ([]models.MessageCitation, error)
	FindAnswer(ctx context.Context, tenantID, userID, conversationID, requestID string) (models.Message, bool, error)
	LatestQuestion(ctx context.Context, tenantID, userID, conversationID, requestID string) (string, bool, error)
	History(ctx context.Context, tenantID, userID, conversationID string, limit int) ([]models.Message, error)
	CountRoleSince(ctx context.Context, tenantID, role string, since time.Time) (int64, error)
}

type PostgresMessageStore struct {
	db *gorm.DB
}

func NewPostgresMessageStore(db *gorm.DB) *PostgresMessageStore {
	return &PostgresMessageStore{db: db}
}

func (s *PostgresMessageStore) Create(ctx context.Context, message models.Message) error {
	return s.db.WithContext(ctx).Create(&message).Error
}

func (s *PostgresMessageStore) CreateCitations(ctx context.Context, citations []models.MessageCitation) error {
	for _, citation := range citations {
		row := citation
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return fmt.Errorf("persist citation: %w", err)
		}
	}
	return nil
}

func (s *PostgresMessageStore) Citations(ctx context.Context, tenantID, messageID string) ([]models.MessageCitation, error) {
	var rows []models.MessageCitation
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Where("message_id = ?", messageID).Order("citation_index").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load citations: %w", err)
	}
	return rows, nil
}

func (s *PostgresMessageStore) FindAnswer(ctx context.Context, tenantID, userID, conversationID, requestID string) (models.Message, bool, error) {
	var message models.Message
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Where("user_id = ? AND conversation_id = ? AND request_id = ? AND role = ?",
			userID, conversationID, requestID, MessageRoleAssistant).
		First(&message).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.Message{}, false, nil
		}
		return models.Message{}, false, fmt.Errorf("load recorded answer: %w", err)
	}
	return message, true, nil
}

func (s *PostgresMessageStore) LatestQuestion(ctx context.Context, tenantID, userID, conversationID, requestID string) (string, bool, error) {
	var message models.Message
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Where("user_id = ? AND conversation_id = ? AND request_id = ? AND role = ?",
			userID, conversationID, requestID, MessageRoleUser).
		Order("created_at DESC, id DESC").First(&message).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("load recorded question: %w", err)
	}
	return message.Content, true, nil
}

func (s *PostgresMessageStore) History(ctx context.Context, tenantID, userID, conversationID string, limit int) ([]models.Message, error) {
	var rows []models.Message
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).
		Where("conversation_id = ? AND user_id = ?", conversationID, userID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load conversation history: %w", err)
	}
	return rows, nil
}

func (s *PostgresMessageStore) CountRoleSince(ctx context.Context, tenantID, role string, since time.Time) (int64, error) {
	var count int64
	if err := tenantdb.MustScoped(s.db.WithContext(ctx), tenantID).Model(&models.Message{}).
		Where("role = ? AND created_at >= ?", role, since).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("check daily chat quota: %w", err)
	}
	return count, nil
}
