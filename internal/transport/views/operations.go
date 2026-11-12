package views

import (
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TaskInspection(view ingestion.TaskInspection) httpapi.TaskInspection {
	result := httpapi.TaskInspection{
		Task:       view.Task,
		Kind:       view.Kind,
		Mechanisms: MechanismStates(view.Mechanisms),
		Outbox:     OutboxEvents(view.Outbox),
	}
	if view.Inbox != nil {
		inbox := TaskInbox(*view.Inbox)
		result.Inbox = &inbox
	}
	if view.Pipeline != nil {
		pipeline := PipelineRun(*view.Pipeline)
		result.Pipeline = &pipeline
	}
	if view.Stage != nil {
		stage := StageRun(*view.Stage)
		result.Stage = &stage
	}
	if view.DeadLetter != nil {
		deadLetter := TaskDeadLetter(*view.DeadLetter)
		result.DeadLetter = &deadLetter
	}
	return result
}

func MechanismStates(rows []ingestion.MechanismState) []httpapi.MechanismState {
	states := make([]httpapi.MechanismState, 0, len(rows))
	for _, row := range rows {
		states = append(states, httpapi.MechanismState{
			Mechanism:     row.Mechanism,
			Status:        row.Status,
			Attempts:      row.Attempts,
			LeaseUntil:    row.LeaseUntil,
			NextAttemptAt: row.NextAttemptAt,
			LastError:     row.LastError,
			Detail:        row.Detail,
		})
	}
	return states
}

func TaskInbox(row models.TaskInbox) httpapi.TaskInbox {
	return httpapi.TaskInbox{
		IdempotencyKey: row.IdempotencyKey,
		TaskType:       row.TaskType,
		Status:         row.Status,
		StageRunID:     row.StageRunID,
		TenantID:       row.TenantID,
		LeaseOwner:     row.LeaseOwner,
		LeaseUntil:     row.LeaseUntil,
		ProcessedAt:    row.ProcessedAt,
		LastError:      row.LastError,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func OutboxEvent(row models.OutboxEvent) httpapi.OutboxEvent {
	return httpapi.OutboxEvent{
		ID:             row.ID,
		TenantID:       row.TenantID,
		AggregateType:  row.AggregateType,
		AggregateID:    row.AggregateID,
		EventType:      row.EventType,
		IdempotencyKey: row.IdempotencyKey,
		Attempts:       row.Attempts,
		NextAttemptAt:  row.NextAttemptAt,
		LastError:      row.LastError,
		PublishedAt:    row.PublishedAt,
		CreatedAt:      row.CreatedAt,
	}
}

func OutboxEvents(rows []models.OutboxEvent) []httpapi.OutboxEvent {
	items := make([]httpapi.OutboxEvent, 0, len(rows))
	for _, row := range rows {
		items = append(items, OutboxEvent(row))
	}
	return items
}

func TaskDeadLetter(row models.TaskDeadLetter) httpapi.TaskDeadLetter {
	return httpapi.TaskDeadLetter{
		ID:                row.ID,
		IdempotencyKey:    row.IdempotencyKey,
		TenantID:          row.TenantID,
		KnowledgeBaseID:   row.KnowledgeBaseID,
		DocumentVersionID: row.DocumentVersionID,
		TaskType:          row.TaskType,
		Stage:             row.Stage,
		Attempts:          row.Attempts,
		LastError:         row.LastError,
		Status:            row.Status,
		ResolvedAt:        row.ResolvedAt,
		ResolvedBy:        row.ResolvedBy,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func TaskDeadLetters(rows []models.TaskDeadLetter) []httpapi.TaskDeadLetter {
	items := make([]httpapi.TaskDeadLetter, 0, len(rows))
	for _, row := range rows {
		items = append(items, TaskDeadLetter(row))
	}
	return items
}

func ReconcileReport(view ingestion.ReconcileReport) httpapi.ReconcileReport {
	return httpapi.ReconcileReport{
		DocumentsRepaired:   view.DocumentsRepaired,
		VersionsRepaired:    view.VersionsRepaired,
		StagesRequeued:      view.StagesRequeued,
		PipelinesCancelled:  view.PipelinesCancelled,
		InboxesExpired:      view.InboxesExpired,
		DeadLettersResolved: view.DeadLettersResolved,
	}
}
