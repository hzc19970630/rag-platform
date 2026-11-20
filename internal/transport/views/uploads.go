package views

import (
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
)

func UploadSession(result ingestion.UploadSessionResult) httpapi.UploadSessionView {
	session := result.Session
	view := httpapi.UploadSessionView{
		ID:              session.ID,
		KnowledgeBaseID: session.KnowledgeBaseID,
		DocumentID:      session.DocumentID,
		Filename:        session.Filename,
		SizeBytes:       session.SizeBytes,
		PartSize:        session.PartSize,
		PartCount:       session.PartCount,
		Transport:       session.Transport,
		Status:          session.Status,
		ExpiresAt:       session.ExpiresAt,
		CreatedAt:       session.CreatedAt,
		CompletedAt:     session.CompletedAt,
		ErrorCode:       session.ErrorCode,
		ErrorMessage:    session.ErrorMessage,
	}
	if len(result.Parts) > 0 {
		view.Parts = make([]httpapi.UploadPartView, 0, len(result.Parts))
		for _, part := range result.Parts {
			view.Parts = append(view.Parts, httpapi.UploadPartView{
				Number:    part.Number,
				SizeBytes: part.SizeBytes,
				Uploaded:  part.Uploaded,
				URL:       part.URL,
			})
		}
	}
	return view
}
