package httpapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/conversation"
)

// tailTimeoutCode is a transport error, not an answer outcome: the job may
// still be running and the client can reconnect to the events route.
const tailTimeoutCode = "tail_timeout"

// tailAnswerStream replays persisted events until a terminal event arrives or
// the tail budget is spent. Every event is already durable, so a reconnect
// resumes from the last sequence without re-running the answer.
func tailAnswerStream(ctx context.Context, service *conversation.Service, principal authz.Principal, requestID string, after int, cfg config.Config, stream *sseStream) {
	poll := time.Duration(cfg.Answer.PollIntervalMS) * time.Millisecond
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	deadline := time.Now().Add(time.Duration(cfg.Answer.TailTimeoutSec) * time.Second)
	for {
		events, err := service.ReplayEvents(ctx, principal, requestID, after)
		if err != nil {
			_ = stream.emit(conversation.Event{RequestID: requestID, Type: conversation.EventError, Data: map[string]any{"code": "internal_error", "message": err.Error()}})
			return
		}
		for _, event := range events {
			if err := stream.emit(event); err != nil {
				return
			}
			after = event.Sequence
			if event.Type == conversation.EventComplete || event.Type == conversation.EventError {
				return
			}
		}
		if !time.Now().Before(deadline) {
			_ = stream.emit(conversation.Event{RequestID: requestID, Type: conversation.EventError, Data: map[string]any{
				"code":    tailTimeoutCode,
				"message": "the answer is still running; reconnect to /api/v1/requests/" + requestID + "/events",
			}})
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// streamRecordedAnswer synthesizes the two terminal events when the replay
// buffer has already been purged but the answer itself is durable. Sequences
// continue after whatever the replay already emitted.
func streamRecordedAnswer(stream *sseStream, result conversation.AnswerResult, after int) {
	citations := result.Citations
	if citations == nil {
		citations = []conversation.CitationView{}
	}
	_ = stream.emit(conversation.Event{
		RequestID: result.RequestID, Sequence: after + 1, Type: conversation.EventAnswer,
		Data: map[string]any{
			"content": result.Content, "citations": citations, "status": result.Status,
			"validation": result.Validation, "evidenceChunkIds": evidenceIDs(result),
		},
	})
	_ = stream.emit(conversation.Event{
		RequestID: result.RequestID, Sequence: after + 2, Type: conversation.EventComplete,
		Data: map[string]any{"status": result.Status, "code": result.ErrorCode, "latencyMs": result.LatencyMS},
	})
}

func evidenceIDs(result conversation.AnswerResult) []string {
	ids := make([]string, 0, len(result.EvidenceSet.Items))
	for _, item := range result.EvidenceSet.Items {
		ids = append(ids, item.ChunkID)
	}
	return ids
}

// logAnswerStream keeps one summary line per tail, matching the synchronous
// handler's answer_completed log without needing the result value.
func logAnswerStream(logger *slog.Logger, requestID string, started time.Time) {
	logger.Info("answer_stream_finished", "request_id", requestID, "duration_ms", time.Since(started).Milliseconds())
}
