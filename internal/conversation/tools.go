package conversation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/agenttools"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/token"
	"github.com/hzc19970630/rag-platform/internal/prompts"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
	"github.com/hzc19970630/rag-platform/internal/tableqa"
)

const toolInstruction = "\n\nYou may call the read-only tools. For any question about tables, sheets, rows, columns, totals or averages, call list_tables and table_query before answering; never infer row values from retrieved snippets. Cite every factual sentence with the evidence index in square brackets, e.g. [1]."

// citationRetryInstruction is one bounded second chance for a tool-backed
// answer the citation validator would reject: models drop [n] markers even
// though the tool results carry them, and refusing a correct table value over
// formatting is worse than asking once more.
const citationRetryInstruction = "上一条回答没有包含证据引用。请基于已返回的工具结果重写答案：每个事实句都必须在句末标明对应证据的方括号序号（如 [1]），且只能使用工具结果中出现的序号。"

func (s *Service) toolsEnabled(ctx context.Context, scope authz.AuthorizationScope, knowledgeBaseID, chatModel string) bool {
	if s.tools == nil || s.tools.Registry() == nil {
		return false
	}
	if !model.ChatterSupportsToolsFor(ctx, s.chat, chatModel) {
		return false
	}
	return s.tools.Enabled(ctx, scope, knowledgeBaseID)
}

// generateAnswer keeps the legacy single-pass path when tools are disabled and
// runs the bounded tool loop otherwise.
func (s *Service) generateAnswer(ctx context.Context, query string, registry *Registry, evidence retrieval.EvidenceSet, history []model.Turn, chatModel string, principal authz.Principal, scope authz.AuthorizationScope, knowledgeBaseID string, toolMode bool, emitEvent func(string, map[string]any) error) (model.ChatResponse, retrieval.EvidenceSet, error) {
	if !toolMode {
		response, err := s.generate(ctx, query, registry, evidence, history, chatModel)
		return response, evidence, err
	}
	return s.generateWithTools(ctx, query, registry, evidence, history, chatModel, principal, scope, knowledgeBaseID, emitEvent)
}

func (s *Service) generateWithTools(ctx context.Context, query string, registry *Registry, evidence retrieval.EvidenceSet, history []model.Turn, chatModel string, principal authz.Principal, scope authz.AuthorizationScope, knowledgeBaseID string, emitEvent func(string, map[string]any) error) (model.ChatResponse, retrieval.EvidenceSet, error) {
	runtime := s.tools
	tools := runtime.Registry().Specs()
	instruction, err := s.systemInstruction(evidence)
	if err != nil {
		return model.ChatResponse{}, evidence, err
	}
	instruction += toolInstruction
	contextTemplate, ok := s.prompts.Raw(prompts.ContextTemplate)
	if !ok {
		return model.ChatResponse{}, evidence, fmt.Errorf("context template is not configured")
	}
	items := evidenceItems(registry)
	current := append([]model.Turn{}, history...)
	calls := 0
	toolEvidence := false
	var last model.ChatResponse
	route := tableqa.Classify(query).Route
	for round := 0; round <= s.toolRounds; round++ {
		toolChoice := "auto"
		if round == 0 && (route == tableqa.RouteLookup || route == tableqa.RouteAggregate) {
			// Table questions must go through the structured path instead of
			// trusting whatever row snippets retrieval happened to return.
			toolChoice = "required"
		}
		request := model.ChatRequest{
			SystemInstruction: instruction, Query: query, History: current,
			Tools: tools, ToolChoice: toolChoice, Model: chatModel,
		}
		if round == 0 {
			request.ContextTemplate = contextTemplate
			request.Evidence = items
		} else {
			request.ContextTemplate = "{{query}}"
		}
		response, err := s.chat.Complete(ctx, request)
		if err != nil && toolChoice == "required" {
			// Reasoning models (DeepSeek thinking mode and similar) reject a
			// forced tool_choice with 400. Degrade once to "auto" instead of
			// failing the answer; the prompt still asks for table_query first.
			request.ToolChoice = "auto"
			response, err = s.chat.Complete(ctx, request)
		}
		if err != nil {
			return model.ChatResponse{}, evidence, err
		}
		last = response
		if len(response.ToolCalls) == 0 {
			return s.retryWithCitations(ctx, registry, instruction, query, chatModel, current, response, toolEvidence, evidence), evidence, nil
		}
		current = append(current, model.Turn{Role: "assistant", Content: response.Text, ToolCalls: response.ToolCalls})
		for _, call := range response.ToolCalls {
			if calls >= s.toolCalls {
				current = append(current, model.Turn{
					Role: "tool", ToolCallID: call.ID, Name: call.Name,
					Content: `{"error":"tool_call_limit","message":"tool call budget exhausted"}`,
				})
				continue
			}
			calls++
			if err := emitEvent(EventToolCall, map[string]any{
				"tool": call.Name, "callId": call.ID, "arguments": truncateRunes(call.Arguments, 512),
			}); err != nil {
				return model.ChatResponse{}, evidence, err
			}
			toolTimeout := s.toolTimeout
			if toolTimeout <= 0 {
				toolTimeout = 20 * time.Second
			}
			toolCtx, cancel := context.WithTimeout(ctx, toolTimeout)
			outcome := runtime.Registry().Invoke(toolCtx, call.Name, agenttools.Scope{
				Principal: principal, Authorization: scope,
				KnowledgeBaseID: knowledgeBaseID, TenantID: principal.TenantID,
			}, call.Arguments)
			cancel()
			content := outcome.Content
			if len(outcome.Evidence) > 0 {
				toolEvidence = true
			}
			for _, item := range outcome.Evidence {
				candidate := toolCandidate(principal, knowledgeBaseID, call.ID, item)
				index := registry.Add(candidate)
				evidence.Items = append(evidence.Items, candidate)
				items = append(items, model.EvidenceItem{
					Index: index, ChunkID: candidate.ChunkID,
					HeadingPath: candidate.HeadingPath, Text: candidate.Content,
				})
				evidence.TokenEstimate += token.Estimate(item.Text)
				content = appendEvidenceBlock(content, index, item)
			}
			if err := emitEvent(EventToolResult, map[string]any{
				"tool": call.Name, "callId": call.ID, "isError": outcome.IsError,
				"truncated": outcome.Truncated, "evidence": len(outcome.Evidence),
				"contentLength": len(outcome.Content),
			}); err != nil {
				return model.ChatResponse{}, evidence, err
			}
			current = append(current, model.Turn{Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: content})
		}
	}
	// The tool budget ran out while the model was still calling tools. Ask once
	// more without tools so the answer is text built from the collected rows
	// instead of an empty tool-call response the citation validator rejects.
	final, finalErr := s.chat.Complete(ctx, model.ChatRequest{
		SystemInstruction: instruction, Query: query, History: current, Model: chatModel,
		ContextTemplate: "{{query}}",
	})
	if finalErr != nil {
		return last, evidence, nil
	}
	return s.retryWithCitations(ctx, registry, instruction, query, chatModel, current, final, toolEvidence, evidence), evidence, nil
}

// retryWithCitations gives one bounded second chance when a tool-backed answer
// would fail citation validation: models drop [n] markers even though the tool
// results carry them, and refusing a correct table value over formatting is
// worse than asking once more with the available indexes spelled out.
func (s *Service) retryWithCitations(ctx context.Context, registry *Registry, instruction, query, chatModel string, history []model.Turn, response model.ChatResponse, toolEvidence bool, evidence retrieval.EvidenceSet) model.ChatResponse {
	if !toolEvidence || !shouldRetryCitation(response.Text, evidence) {
		return response
	}
	retryNote := citationRetryInstruction
	if indexes := registry.Indexes(); len(indexes) > 0 {
		parts := make([]string, len(indexes))
		for position, index := range indexes {
			parts[position] = fmt.Sprintf("[%d]", index)
		}
		retryNote += " 可用序号：" + strings.Join(parts, "、")
	}
	turns := append(append([]model.Turn{}, history...),
		model.Turn{Role: "assistant", Content: response.Text},
		model.Turn{Role: "user", Content: retryNote},
	)
	corrected, err := s.chat.Complete(ctx, model.ChatRequest{
		SystemInstruction: instruction, Query: query, History: turns, Model: chatModel,
		ContextTemplate: "{{query}}",
	})
	if err != nil {
		return response
	}
	return corrected
}

func shouldRetryCitation(answer string, evidence retrieval.EvidenceSet) bool {
	validation := Validate(answer, evidence)
	return !validation.Valid && (validation.Ungrounded || len(validation.InvalidIndexes) > 0)
}

func evidenceItems(registry *Registry) []model.EvidenceItem {
	indexes := registry.Indexes()
	items := make([]model.EvidenceItem, 0, len(indexes))
	for _, index := range indexes {
		candidate, ok := registry.Lookup(index)
		if !ok {
			continue
		}
		items = append(items, model.EvidenceItem{
			Index: index, ChunkID: candidate.ChunkID,
			HeadingPath: candidate.HeadingPath, Text: candidate.Content,
		})
	}
	return items
}

func toolCandidate(principal authz.Principal, knowledgeBaseID, callID string, item agenttools.Evidence) retrieval.Candidate {
	chunkID := item.ChunkID
	if chunkID == "" {
		if item.TableAssetID != "" {
			chunkID = fmt.Sprintf("table:%s:%d-%d", item.TableAssetID, item.RowStart, item.RowEnd)
		} else {
			chunkID = "tool:" + callID
		}
	}
	kbID := item.KnowledgeBaseID
	if kbID == "" {
		kbID = knowledgeBaseID
	}
	return retrieval.Candidate{
		ChunkID: chunkID, DocumentID: item.DocumentID, DocumentVersionID: item.DocumentVersionID,
		KnowledgeBaseID: kbID, Content: item.Text, HeadingPath: item.HeadingPath,
		PageNumber: item.PageNumber, SourcePath: item.SourcePath, Channels: []string{"tool"},
		EvidenceKind: retrieval.EvidenceKindRow, TableAssetID: item.TableAssetID,
		Sheet: item.Sheet, RowStart: item.RowStart, RowEnd: item.RowEnd,
	}
}

func appendEvidenceBlock(content string, index int, item agenttools.Evidence) string {
	text := item.Text
	if strings.TrimSpace(text) == "" {
		text = item.Sheet
	}
	return strings.TrimRight(content, "\n") + fmt.Sprintf("\n[%d] %s", index, text)
}
