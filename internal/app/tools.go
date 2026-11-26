package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/agenttools"
	"github.com/hzc19970630/rag-platform/internal/agenttools/builtin"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

// registerBuiltinTools wires the first-party executors into the shared
// registry. The specs come from builtin.Specs so MCP sees the same schema.
func registerBuiltinTools(registry *agenttools.Registry, tables *ingestion.Service, retriever *retrieval.Service) error {
	specs := map[string]model.ToolSpec{}
	for _, spec := range builtin.Specs() {
		specs[spec.Name] = spec
	}
	executors := []agenttools.Tool{
		{Spec: specs[builtin.SearchKnowledge], Execute: searchKnowledgeTool(retriever)},
		{Spec: specs[builtin.ListDocuments], Execute: listDocumentsTool(tables)},
		{Spec: specs[builtin.ListTables], Execute: listTablesTool(tables)},
		{Spec: specs[builtin.TableSchema], Execute: tableSchemaTool(tables)},
		{Spec: specs[builtin.TableQuery], Execute: tableQueryTool(tables)},
	}
	for _, tool := range executors {
		if err := registry.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func searchKnowledgeTool(retriever *retrieval.Service) func(context.Context, agenttools.Call) (agenttools.Result, error) {
	return func(ctx context.Context, call agenttools.Call) (agenttools.Result, error) {
		query, _ := call.Parsed["query"].(string)
		if strings.TrimSpace(query) == "" {
			return agenttools.Result{}, fmt.Errorf("query must not be empty")
		}
		limit := boundedInt(call.Parsed["limit"], 8, 20)
		result, err := retriever.Retrieve(ctx, call.Scope.Authorization, retrieval.QueryInput{
			Query: query, KnowledgeBaseID: call.Scope.KnowledgeBaseID,
		})
		if err != nil {
			return agenttools.Result{}, err
		}
		evidence := make([]agenttools.Evidence, 0, limit)
		lines := make([]string, 0, limit+1)
		for index, candidate := range result.Items {
			if index >= limit {
				break
			}
			evidence = append(evidence, agenttools.Evidence{
				Text: candidate.Content, ChunkID: candidate.ChunkID,
				DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID,
				KnowledgeBaseID: candidate.KnowledgeBaseID, HeadingPath: candidate.HeadingPath,
				PageNumber: candidate.PageNumber, SourcePath: candidate.SourcePath,
			})
			lines = append(lines, fmt.Sprintf("doc://%s %s", candidate.DocumentID, candidate.HeadingPath))
		}
		if len(evidence) == 0 {
			return agenttools.Result{Content: "no evidence matched the query"}, nil
		}
		return agenttools.Result{Content: strings.Join(lines, "\n"), Data: result, Evidence: evidence}, nil
	}
}

func listDocumentsTool(tables *ingestion.Service) func(context.Context, agenttools.Call) (agenttools.Result, error) {
	return func(ctx context.Context, call agenttools.Call) (agenttools.Result, error) {
		limit := boundedInt(call.Parsed["limit"], 20, 50)
		cursor, _ := call.Parsed["cursor"].(string)
		page, err := tables.ListDocuments(ctx, call.Scope.Principal, call.Scope.KnowledgeBaseID, ingestion.DocumentFilter{}, cursor, limit)
		if err != nil {
			return agenttools.Result{}, err
		}
		lines := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			lines = append(lines, fmt.Sprintf("doc://%s %s status=%s", item.Document.ID, item.Document.Filename, item.Document.Status))
		}
		if len(lines) == 0 {
			lines = append(lines, "no documents")
		}
		if page.NextCursor != "" {
			lines = append(lines, "next_cursor="+page.NextCursor)
		}
		return agenttools.Result{Content: strings.Join(lines, "\n"), Data: page}, nil
	}
}

func listTablesTool(tables *ingestion.Service) func(context.Context, agenttools.Call) (agenttools.Result, error) {
	return func(ctx context.Context, call agenttools.Call) (agenttools.Result, error) {
		assets, err := tables.ListTableAssets(ctx, call.Scope.Principal, call.Scope.KnowledgeBaseID)
		if err != nil {
			return agenttools.Result{}, err
		}
		lines := make([]string, 0, len(assets))
		for _, asset := range assets {
			header := decodeStrings(asset.Header)
			lines = append(lines, fmt.Sprintf(
				"asset://%s sheet=%s rows=%d/%d columns=%s materialize=%s",
				asset.ID, asset.Sheet, asset.ParsedRows, asset.TotalRows, strings.Join(header, ","), asset.MaterializeStatus,
			))
		}
		if len(lines) == 0 {
			lines = append(lines, "no table assets")
		}
		return agenttools.Result{Content: strings.Join(lines, "\n"), Data: assets}, nil
	}
}

func tableSchemaTool(tables *ingestion.Service) func(context.Context, agenttools.Call) (agenttools.Result, error) {
	return func(ctx context.Context, call agenttools.Call) (agenttools.Result, error) {
		assetID := assetIDFrom(call.Parsed["assetId"])
		asset, err := findAsset(ctx, tables, call, assetID)
		if err != nil {
			return agenttools.Result{}, err
		}
		var schema struct {
			ColumnTypes []string `json:"columnTypes"`
		}
		_ = json.Unmarshal([]byte(asset.SchemaJSON), &schema)
		payload := map[string]any{
			"assetId": asset.ID, "sheet": asset.Sheet, "columns": decodeStrings(asset.Header),
			"columnTypes": schema.ColumnTypes, "rowCount": asset.RowCount,
			"totalRows": asset.TotalRows, "parsedRows": asset.ParsedRows,
			"storageMode": asset.StorageMode, "materializeStatus": asset.MaterializeStatus,
			"truncated": asset.Truncated,
		}
		encoded, _ := json.Marshal(payload)
		return agenttools.Result{Content: string(encoded), Data: payload}, nil
	}
}

func tableQueryTool(tables *ingestion.Service) func(context.Context, agenttools.Call) (agenttools.Result, error) {
	return func(ctx context.Context, call agenttools.Call) (agenttools.Result, error) {
		question, _ := call.Parsed["question"].(string)
		if strings.TrimSpace(question) == "" {
			return agenttools.Result{}, fmt.Errorf("question must not be empty")
		}
		result, err := tables.AskTableSQL(ctx, call.Scope.Principal, call.Scope.KnowledgeBaseID, ingestion.TableAskSQLInput{
			Question: question, AssetID: assetIDFrom(call.Parsed["assetId"]),
		})
		if err != nil {
			return agenttools.Result{}, err
		}
		if result.Result == nil {
			return agenttools.Result{}, fmt.Errorf("table_query_no_result: %s", result.FallbackReason)
		}
		content, evidence := tableAgentResult(result.Result, call.Scope.KnowledgeBaseID)
		return agenttools.Result{Content: content, Data: result, Evidence: evidence}, nil
	}
}

func tableAgentResult(answer *ingestion.TableAskResult, knowledgeBaseID string) (string, []agenttools.Evidence) {
	lines := make([]string, 0, len(answer.Rows)+2)
	lines = append(lines, fmt.Sprintf("operation=%s matchedRows=%d", answer.Operation, answer.MatchedRows))
	if answer.Value != nil {
		lines = append(lines, fmt.Sprintf("value=%v", *answer.Value))
	}
	evidence := make([]agenttools.Evidence, 0, minInt(len(answer.Rows), 20))
	for index, row := range answer.Rows {
		line := rowLine(row)
		lines = append(lines, line)
		if index >= 20 {
			continue
		}
		evidence = append(evidence, agenttools.Evidence{
			Text: line, DocumentID: answer.DocumentID, KnowledgeBaseID: knowledgeBaseID,
			TableAssetID: answer.AssetID, Sheet: answer.Sheet,
			RowStart: row.RowNumber, RowEnd: row.RowNumber,
			SourcePath: fmt.Sprintf("sheet:%s!row:%d", answer.Sheet, row.RowNumber),
		})
	}
	if len(evidence) == 0 && answer.Value != nil {
		evidence = append(evidence, agenttools.Evidence{
			Text: fmt.Sprintf("value=%v", *answer.Value), DocumentID: answer.DocumentID,
			KnowledgeBaseID: knowledgeBaseID, TableAssetID: answer.AssetID,
			Sheet: answer.Sheet, SourcePath: "sheet:" + answer.Sheet,
		})
	}
	return strings.Join(lines, "\n"), evidence
}

// tableFallback is the deterministic path for models without function calling:
// it executes one table query and hands the rows to the normal generator.
type tableFallback struct {
	tables *ingestion.Service
}

func (f tableFallback) Answer(ctx context.Context, principal authz.Principal, knowledgeBaseID, question string) (string, []agenttools.Evidence, bool) {
	if f.tables == nil {
		return "", nil, false
	}
	assets, err := f.tables.ListTableAssets(ctx, principal, knowledgeBaseID)
	if err != nil || len(assets) == 0 {
		return "", nil, false
	}
	result, err := f.tables.AskTableSQL(ctx, principal, knowledgeBaseID, ingestion.TableAskSQLInput{Question: question})
	if err != nil || result.Result == nil {
		return "", nil, false
	}
	content, evidence := tableAgentResult(result.Result, knowledgeBaseID)
	return content, evidence, len(evidence) > 0
}

func findAsset(ctx context.Context, tables *ingestion.Service, call agenttools.Call, assetID string) (models.TableAsset, error) {
	if assetID == "" {
		return models.TableAsset{}, fmt.Errorf("assetId is required")
	}
	assets, err := tables.ListTableAssets(ctx, call.Scope.Principal, call.Scope.KnowledgeBaseID)
	if err != nil {
		return models.TableAsset{}, err
	}
	for _, asset := range assets {
		if asset.ID == assetID {
			return asset, nil
		}
	}
	return models.TableAsset{}, fmt.Errorf("asset_not_found: %s", assetID)
}

func assetIDFrom(value any) string {
	raw, _ := value.(string)
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "asset://")
	return raw
}

func decodeStrings(encoded string) []string {
	var values []string
	_ = json.Unmarshal([]byte(encoded), &values)
	return values
}

func rowLine(row ingestion.TableRowView) string {
	keys := make([]string, 0, len(row.Cells))
	for key := range row.Cells {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+row.Cells[key])
	}
	return fmt.Sprintf("row %d: %s", row.RowNumber, strings.Join(parts, ", "))
}

func boundedInt(value any, fallback, maximum int) int {
	var parsed int64
	switch number := value.(type) {
	case json.Number:
		value, err := number.Int64()
		if err != nil {
			return fallback
		}
		parsed = value
	case float64:
		parsed = int64(number)
	default:
		return fallback
	}
	if parsed <= 0 {
		return fallback
	}
	if parsed > int64(maximum) {
		return maximum
	}
	return int(parsed)
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
