package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/apperror"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/example/rag-platform/internal/platform/formats"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/example/rag-platform/internal/platform/secrets"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	cursorPhaseItems      = "items"
	cursorPhaseTombstones = "tombstones"
	cursorPhaseDone       = "done"

	syncLease = 10 * time.Minute
)

type cursorState struct {
	ManifestHash string `json:"manifestHash,omitempty"`
	Position     int    `json:"position"`
	Phase        string `json:"phase,omitempty"`
}

type syncCounters struct {
	total   int
	created int
	updated int
	deleted int
	skipped int
	failed  int
}

// Worker executes one durable sync run. It only converts remote objects into
// document versions through ingestion.Service: it never writes chunks,
// embeddings or search indexes itself.
type Worker struct {
	db        *gorm.DB
	cipher    *secrets.Cipher
	fetcher   *Fetcher
	ingestion *ingestion.Service
}

func NewWorker(db *gorm.DB, cipher *secrets.Cipher, fetcher *Fetcher, ingestionService *ingestion.Service) *Worker {
	return &Worker{db: db, cipher: cipher, fetcher: fetcher, ingestion: ingestionService}
}

func (w *Worker) Process(ctx context.Context, task SyncTask) error {
	if strings.TrimSpace(task.SyncRunID) == "" || strings.TrimSpace(task.TenantID) == "" || strings.TrimSpace(task.ConnectorID) == "" {
		return fmt.Errorf("sync task is missing its run identity")
	}
	var run models.SyncRun
	if err := w.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND connector_id = ?", task.SyncRunID, task.TenantID, task.ConnectorID).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("load sync run: %w", err)
	}
	if !runActive(run.Status) {
		return nil
	}
	var connector models.Connector
	if err := w.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", task.TenantID, task.ConnectorID).First(&connector).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// The connector and its runs are gone; nothing left to do.
			return nil
		}
		return fmt.Errorf("load connector: %w", err)
	}
	state := decodeCursor(run.Cursor)
	counters := countersFrom(run)
	if !connector.Enabled {
		return w.finishRun(ctx, connector, run, StatusCancelled, state, counters, appendSummary(run.ErrorSummary, "", "connector is disabled"), false)
	}
	config := decodeConfig(connector.Config)
	manifestURL, err := ParseManifestURL(config.ManifestURL)
	if err != nil {
		return w.failTerminal(ctx, connector, run, state, counters, "invalid connector config: "+err.Error())
	}
	credential, err := w.decryptCredential(connector.CredentialRef)
	if err != nil {
		return w.failTerminal(ctx, connector, run, state, counters, err.Error())
	}

	now := time.Now()
	lease := now.Add(syncLease)
	claim := w.db.WithContext(ctx).Model(&models.SyncRun{}).
		Where("id = ? AND status IN ?", run.ID, []string{StatusPending, StatusRunning}).
		Updates(map[string]any{
			"status": StatusRunning, "lease_until": &lease, "attempts": gorm.Expr("attempts + 1"), "updated_at": now,
		})
	if claim.Error != nil {
		return fmt.Errorf("claim sync run: %w", claim.Error)
	}
	if claim.RowsAffected == 0 {
		return nil
	}
	if run.StartedAt == nil {
		if err := w.db.WithContext(ctx).Model(&models.SyncRun{}).Where("id = ?", run.ID).Update("started_at", &now).Error; err != nil {
			return fmt.Errorf("record sync start: %w", err)
		}
	}
	run.Status = StatusRunning
	run.Attempts++

	if cancelled, err := w.cancelRequested(ctx, run.ID); err != nil {
		return err
	} else if cancelled {
		return w.finishRun(ctx, connector, run, StatusCancelled, state, counters, appendSummary(run.ErrorSummary, "", "cancelled by operator"), false)
	}

	manifest, hash, err := w.fetcher.FetchManifest(ctx, connector.ID, manifestURL, credential, config.RequestsPerSecond)
	if err != nil {
		if IsRetryable(err) {
			return err
		}
		return w.failTerminal(ctx, connector, run, state, counters, err.Error())
	}
	if len(manifest.Items) > config.MaxItemsPerRun {
		return w.failTerminal(ctx, connector, run, state, counters,
			fmt.Sprintf("manifest contains %d items, above the configured limit of %d", len(manifest.Items), config.MaxItemsPerRun))
	}
	if state.ManifestHash != hash || state.Position < 0 || state.Position > len(manifest.Items) {
		state = cursorState{ManifestHash: hash, Phase: cursorPhaseItems}
	}
	if state.Phase == "" {
		state.Phase = cursorPhaseItems
	}
	counters.total = len(manifest.Items)

	mappings, err := w.loadMappings(ctx, connector.ID)
	if err != nil {
		return err
	}
	present := make(map[string]struct{}, len(manifest.Items))
	for _, item := range manifest.Items {
		present[item.ExternalID] = struct{}{}
	}
	summary := run.ErrorSummary

	for index := state.Position; index < len(manifest.Items); index++ {
		if err := ctx.Err(); err != nil {
			_ = w.saveProgress(context.Background(), run.ID, state, counters, summary)
			return err
		}
		if cancelled, err := w.cancelRequested(ctx, run.ID); err != nil {
			return err
		} else if cancelled {
			return w.finishRun(ctx, connector, run, StatusCancelled, state, counters, appendSummary(summary, "", "cancelled by operator"), false)
		}
		item := manifest.Items[index]
		state.Phase = cursorPhaseItems
		mapping := mappings[item.ExternalID]
		if mapping != nil && mapping.Status == ItemActive && mapping.DocumentID != nil && unchangedByMetadata(*mapping, item) {
			counters.skipped++
			state.Position = index + 1
			if err := w.saveProgress(ctx, run.ID, state, counters, summary); err != nil {
				return err
			}
			continue
		}
		filename := ItemFilename(item)
		if !supportedFilename(filename) {
			counters.failed++
			summary = appendSummary(summary, item.ExternalID, fmt.Errorf("unsupported file type"))
			state.Position = index + 1
			if err := w.saveProgress(ctx, run.ID, state, counters, summary); err != nil {
				return err
			}
			continue
		}
		file, err := w.fetcher.FetchFile(ctx, connector.ID, item, manifestURL, credential, config.RequestsPerSecond)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				_ = w.saveProgress(context.Background(), run.ID, state, counters, summary)
				return err
			}
			if IsRetryable(err) {
				if saveErr := w.saveProgress(ctx, run.ID, state, counters, summary); saveErr != nil {
					return saveErr
				}
				return err
			}
			counters.failed++
			summary = appendSummary(summary, item.ExternalID, err)
			state.Position = index + 1
			if err := w.saveProgress(ctx, run.ID, state, counters, summary); err != nil {
				return err
			}
			continue
		}
		if mapping != nil && mapping.Status == ItemActive && mapping.DocumentID != nil && mapping.ContentHash == file.ContentHash {
			counters.skipped++
			state.Position = index + 1
			if err := w.saveProgress(ctx, run.ID, state, counters, summary); err != nil {
				return err
			}
			continue
		}
		result, err := w.ingestion.IngestExternal(ctx, ingestion.ExternalUploadInput{
			TenantID: connector.TenantID, KnowledgeBaseID: connector.KnowledgeBaseID,
			DocumentID: stableDocumentID(connector.ID, item.ExternalID), Filename: filename, Content: file.Content,
			IdempotencyKey: stableSyncKey(connector.ID, item.ExternalID, file.ContentHash),
			RequestID:      run.ID, TraceID: run.ID, ActorID: connector.CreatedBy,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				_ = w.saveProgress(context.Background(), run.ID, state, counters, summary)
				return err
			}
			if !terminalIngestError(err) {
				if saveErr := w.saveProgress(ctx, run.ID, state, counters, summary); saveErr != nil {
					return saveErr
				}
				return fmt.Errorf("ingest manifest item %q: %w", item.ExternalID, err)
			}
			counters.failed++
			summary = appendSummary(summary, item.ExternalID, err)
			state.Position = index + 1
			if err := w.saveProgress(ctx, run.ID, state, counters, summary); err != nil {
				return err
			}
			continue
		}
		created := mapping == nil || mapping.DocumentID == nil
		itemNow := time.Now()
		documentID := result.Document.ID
		versionID := result.Version.ID
		row := models.ConnectorItem{
			TenantID: connector.TenantID, ConnectorID: connector.ID, ExternalID: item.ExternalID,
			RemotePath: filename, RemoteVersion: item.Version, ContentHash: file.ContentHash,
			DocumentID: &documentID, DocumentVersionID: &versionID, Status: ItemActive, LastSyncedAt: &itemNow,
		}
		if created {
			counters.created++
		} else {
			counters.updated++
		}
		state.Position = index + 1
		err = w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "connector_id"}, {Name: "external_id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"remote_path", "remote_version", "content_hash", "document_id", "document_version_id",
					"status", "last_synced_at", "tombstoned_at", "updated_at",
				}),
			}).Create(&row).Error; err != nil {
				return err
			}
			return w.saveProgressTx(tx, run.ID, state, counters, summary)
		})
		if err != nil {
			return fmt.Errorf("record connector item %q: %w", item.ExternalID, err)
		}
		stored := row
		mappings[item.ExternalID] = &stored
	}

	state.Phase = cursorPhaseTombstones
	state.Position = len(manifest.Items)
	if err := w.saveProgress(ctx, run.ID, state, counters, summary); err != nil {
		return err
	}
	for _, mapping := range mappings {
		if mapping == nil || mapping.Status != ItemActive {
			continue
		}
		if _, exists := present[mapping.ExternalID]; exists {
			continue
		}
		if cancelled, err := w.cancelRequested(ctx, run.ID); err != nil {
			return err
		} else if cancelled {
			return w.finishRun(ctx, connector, run, StatusCancelled, state, counters, appendSummary(summary, "", "cancelled by operator"), false)
		}
		if mapping.DocumentID != nil && *mapping.DocumentID != "" {
			if err := w.ingestion.TombstoneExternal(ctx, connector.TenantID, *mapping.DocumentID, connector.CreatedBy); err != nil && !errors.Is(err, apperror.ErrNotFound) {
				if saveErr := w.saveProgress(ctx, run.ID, state, counters, summary); saveErr != nil {
					return saveErr
				}
				return fmt.Errorf("tombstone document for %q: %w", mapping.ExternalID, err)
			}
		}
		itemNow := time.Now()
		counters.deleted++
		// Persist the counter with the mapping transition. The cursor is already
		// in the tombstone phase, so a retry re-scans remaining ACTIVE mappings
		// without double-counting this one.
		err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.ConnectorItem{}).Where("id = ? AND status = ?", mapping.ID, ItemActive).
				Updates(map[string]any{"status": ItemTombstoned, "tombstoned_at": &itemNow, "updated_at": itemNow}).Error; err != nil {
				return err
			}
			return w.saveProgressTx(tx, run.ID, state, counters, summary)
		})
		if err != nil {
			return fmt.Errorf("tombstone connector item %q: %w", mapping.ExternalID, err)
		}
		mapping.Status = ItemTombstoned
	}
	state.Phase = cursorPhaseDone
	if err := w.saveProgress(ctx, run.ID, state, counters, summary); err != nil {
		return err
	}

	status := StatusSucceeded
	if counters.failed > 0 {
		if counters.created+counters.updated+counters.deleted+counters.skipped > 0 {
			status = StatusPartial
		} else {
			status = StatusFailed
		}
	}
	return w.finishRun(ctx, connector, run, status, state, counters, summary, true)
}

func (w *Worker) failTerminal(ctx context.Context, connector models.Connector, run models.SyncRun, state cursorState, counters syncCounters, message string) error {
	return w.finishRun(ctx, connector, run, StatusFailed, state, counters, appendSummary(run.ErrorSummary, "", message), false)
}

func (w *Worker) finishRun(ctx context.Context, connector models.Connector, run models.SyncRun, status string, state cursorState, counters syncCounters, summary string, touchedSource bool) error {
	now := time.Now()
	updates := map[string]any{
		"status": status, "items_total": counters.total, "items_created": counters.created,
		"items_updated": counters.updated, "items_deleted": counters.deleted, "items_skipped": counters.skipped,
		"items_failed": counters.failed, "error_summary": truncate(summary, 4000), "cursor": encodeCursor(state),
		"finished_at": &now, "lease_until": nil, "updated_at": now,
	}
	if status == StatusCancelled {
		updates["cancel_requested"] = true
	}
	connectorUpdates := map[string]any{"updated_at": now, "next_sync_at": nil}
	if connector.Enabled && connector.SyncIntervalSeconds > 0 {
		next := now.Add(time.Duration(connector.SyncIntervalSeconds) * time.Second)
		connectorUpdates["next_sync_at"] = &next
	}
	if touchedSource {
		connectorUpdates["last_sync_at"] = &now
	}
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.SyncRun{}).Where("id = ? AND status IN ?", run.ID, []string{StatusPending, StatusRunning}).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Model(&models.Connector{}).Where("id = ?", connector.ID).Updates(connectorUpdates).Error
	})
	if err != nil {
		return fmt.Errorf("finish sync run: %w", err)
	}
	return nil
}

func (w *Worker) saveProgress(ctx context.Context, runID string, state cursorState, counters syncCounters, summary string) error {
	return w.saveProgressTx(w.db.WithContext(ctx), runID, state, counters, summary)
}

func (w *Worker) saveProgressTx(tx *gorm.DB, runID string, state cursorState, counters syncCounters, summary string) error {
	now := time.Now()
	lease := now.Add(syncLease)
	return tx.Model(&models.SyncRun{}).Where("id = ?", runID).Updates(map[string]any{
		"items_total": counters.total, "items_created": counters.created, "items_updated": counters.updated,
		"items_deleted": counters.deleted, "items_skipped": counters.skipped, "items_failed": counters.failed,
		"error_summary": truncate(summary, 4000), "cursor": encodeCursor(state), "lease_until": &lease, "updated_at": now,
	}).Error
}

func (w *Worker) cancelRequested(ctx context.Context, runID string) (bool, error) {
	var requested bool
	if err := w.db.WithContext(ctx).Model(&models.SyncRun{}).Select("cancel_requested").Where("id = ?", runID).Scan(&requested).Error; err != nil {
		return false, fmt.Errorf("check sync cancellation: %w", err)
	}
	return requested, nil
}

func (w *Worker) decryptCredential(envelope string) (*Credentials, error) {
	if strings.TrimSpace(envelope) == "" {
		return nil, nil
	}
	raw, err := w.cipher.Decrypt(envelope)
	if err != nil {
		return nil, fmt.Errorf("decrypt connector credential: %w", err)
	}
	var credential Credentials
	if err := json.Unmarshal(raw, &credential); err != nil {
		return nil, fmt.Errorf("decode connector credential: %w", err)
	}
	switch credential.Type {
	case "bearer":
		if credential.Token == "" {
			return nil, fmt.Errorf("stored bearer credential is empty")
		}
	case "basic":
		if credential.Username == "" || credential.Password == "" {
			return nil, fmt.Errorf("stored basic credential is incomplete")
		}
	default:
		return nil, fmt.Errorf("stored connector credential type is unsupported")
	}
	return &credential, nil
}

func (w *Worker) loadMappings(ctx context.Context, connectorID string) (map[string]*models.ConnectorItem, error) {
	var rows []models.ConnectorItem
	if err := w.db.WithContext(ctx).Where("connector_id = ?", connectorID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load connector items: %w", err)
	}
	mappings := make(map[string]*models.ConnectorItem, len(rows))
	for index := range rows {
		mappings[rows[index].ExternalID] = &rows[index]
	}
	return mappings, nil
}

func unchangedByMetadata(mapping models.ConnectorItem, item ManifestItem) bool {
	if item.Version != "" && mapping.RemoteVersion == item.Version {
		return true
	}
	if item.SHA256 != "" && mapping.ContentHash == item.SHA256 {
		return true
	}
	return false
}

func countersFrom(run models.SyncRun) syncCounters {
	return syncCounters{
		total: run.ItemsTotal, created: run.ItemsCreated, updated: run.ItemsUpdated,
		deleted: run.ItemsDeleted, skipped: run.ItemsSkipped, failed: run.ItemsFailed,
	}
}

func runActive(status string) bool {
	return status == StatusPending || status == StatusRunning
}

func decodeCursor(raw string) cursorState {
	state := cursorState{Phase: cursorPhaseItems}
	if strings.TrimSpace(raw) == "" {
		return state
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return cursorState{Phase: cursorPhaseItems}
	}
	if state.Phase == "" {
		state.Phase = cursorPhaseItems
	}
	return state
}

func encodeCursor(state cursorState) string {
	encoded, _ := json.Marshal(state)
	return string(encoded)
}

func appendSummary(summary, externalID string, line any) string {
	message := fmt.Sprint(line)
	if externalID != "" {
		message = externalID + ": " + message
	}
	if strings.TrimSpace(summary) == "" {
		return truncate(message, 4000)
	}
	return truncate(summary+"\n"+message, 4000)
}

func supportedFilename(name string) bool {
	return formats.ExtensionSupported(name)
}

// terminalIngestError separates item-level rejections from infrastructure
// failures. A quota or validation error will not resolve on retry, but a
// database or object-storage error should abort the attempt and preserve the
// cursor for a later retry.
func terminalIngestError(err error) bool {
	if errors.Is(err, apperror.ErrQuotaExceeded) || errors.Is(err, apperror.ErrNotFound) || errors.Is(err, apperror.ErrForbidden) {
		return true
	}
	message := err.Error()
	return strings.Contains(message, "unsupported mime type") || strings.Contains(message, "exceeds maximum size") ||
		strings.Contains(message, "is required") || strings.Contains(message, "must not be")
}
