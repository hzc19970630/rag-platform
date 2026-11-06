package views

import (
	"github.com/example/rag-platform/contracts/httpapi"
	"github.com/example/rag-platform/internal/conversation"
	"github.com/example/rag-platform/internal/platform/persistence/models"
)

func Conversation(row models.Conversation) httpapi.Conversation {
	return httpapi.Conversation{
		ID:              row.ID,
		TenantID:        row.TenantID,
		KnowledgeBaseID: row.KnowledgeBaseID,
		UserID:          row.UserID,
		Title:           row.Title,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func Conversations(rows []models.Conversation) []httpapi.Conversation {
	items := make([]httpapi.Conversation, 0, len(rows))
	for _, row := range rows {
		items = append(items, Conversation(row))
	}
	return items
}

func Message(row models.Message) httpapi.Message {
	return httpapi.Message{
		ID:                        row.ID,
		ConversationID:            row.ConversationID,
		TenantID:                  row.TenantID,
		KnowledgeBaseID:           row.KnowledgeBaseID,
		UserID:                    row.UserID,
		RequestID:                 row.RequestID,
		Role:                      row.Role,
		Content:                   row.Content,
		Status:                    row.Status,
		AuthorizationScopeVersion: row.AuthorizationScopeVersion,
		Model:                     row.Model,
		LatencyMS:                 row.LatencyMS,
		ErrorCode:                 row.ErrorCode,
		ErrorMessage:              row.ErrorMessage,
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
	}
}

func Citation(view conversation.CitationView) httpapi.Citation {
	return httpapi.Citation{
		Index:             view.Index,
		ChunkID:           view.ChunkID,
		DocumentID:        view.DocumentID,
		DocumentVersionID: view.DocumentVersionID,
		KnowledgeBaseID:   view.KnowledgeBaseID,
		HeadingPath:       view.HeadingPath,
		PageNumber:        view.PageNumber,
		SourcePath:        view.SourcePath,
		Quote:             view.Quote,
		Rank:              view.Rank,
		Score:             view.Score,
	}
}

func Citations(rows []conversation.CitationView) []httpapi.Citation {
	items := make([]httpapi.Citation, 0, len(rows))
	for _, row := range rows {
		items = append(items, Citation(row))
	}
	return items
}

func MessageView(view conversation.MessageView) httpapi.MessageView {
	return httpapi.MessageView{
		Message:   Message(view.Message),
		Citations: Citations(view.Citations),
	}
}

func MessageViews(rows []conversation.MessageView) []httpapi.MessageView {
	items := make([]httpapi.MessageView, 0, len(rows))
	for _, row := range rows {
		items = append(items, MessageView(row))
	}
	return items
}

func StreamEvent(event conversation.Event) httpapi.StreamEvent {
	return httpapi.StreamEvent{
		EventID:   event.EventID,
		RequestID: event.RequestID,
		Sequence:  event.Sequence,
		Type:      event.Type,
		Data:      event.Data,
	}
}

func CreateConversationInput(input httpapi.ConversationCreateRequest) conversation.CreateConversationInput {
	return conversation.CreateConversationInput{
		KnowledgeBaseID: input.KnowledgeBaseID,
		Title:           input.Title,
	}
}

func AskInput(conversationID string, input httpapi.MessageCreateRequest, requestID string) conversation.AskInput {
	return conversation.AskInput{
		ConversationID:  conversationID,
		Query:           input.Query,
		RequestID:       requestID,
		SessionOverride: input.SessionOverride,
	}
}
