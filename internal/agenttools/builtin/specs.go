// Package builtin owns the provider-neutral schemas of the first-party tools.
// The conversation loop and the MCP server both render these specs, so a tool
// cannot drift between the two surfaces.
package builtin

import "github.com/hzc19970630/rag-platform/internal/contracts/model"

const (
	SearchKnowledge = "search_knowledge"
	ListDocuments   = "list_documents"
	ListTables      = "list_tables"
	TableSchema     = "table_schema"
	TableQuery      = "table_query"
)

func Specs() []model.ToolSpec {
	return []model.ToolSpec{
		{
			Name: SearchKnowledge, ReadOnly: true,
			Description: "Search the current knowledge base for text evidence. Returns citable chunks with document handles.",
			InputSchema: object(map[string]any{
				"query": stringProperty("the question or keywords"),
				"limit": integerProperty("maximum hits, default 8, max 20"),
			}, "query"),
		},
		{
			Name: ListDocuments, ReadOnly: true,
			Description: "List documents in the current knowledge base with their processing status and doc:// handles.",
			InputSchema: object(map[string]any{
				"limit":  integerProperty("maximum documents, default 20, max 50"),
				"cursor": stringProperty("opaque cursor from a previous call"),
			}),
		},
		{
			Name: ListTables, ReadOnly: true,
			Description: "List table assets in the current knowledge base. Use the returned asset:// handle with table_schema or table_query. MCP callers pass knowledgeBaseId explicitly.",
			InputSchema: object(map[string]any{
				"knowledgeBaseId": stringProperty("knowledge base id; required by MCP, implicit in chat"),
			}),
		},
		{
			Name: TableSchema, ReadOnly: true,
			Description: "Return columns, row counts and coverage for one table asset.",
			InputSchema: object(map[string]any{
				"knowledgeBaseId": stringProperty("knowledge base id; required by MCP, implicit in chat"),
				"assetId":         stringProperty("asset:// handle or asset id returned by list_tables"),
			}, "assetId"),
		},
		{
			Name: TableQuery, ReadOnly: true,
			Description: "Answer one row-level table question (lookup or aggregation) over the immutable columnar copy. Read-only and audited.",
			InputSchema: object(map[string]any{
				"knowledgeBaseId": stringProperty("knowledge base id; required by MCP, implicit in chat"),
				"question":        stringProperty("the table question to answer"),
				"assetId":         stringProperty("optional asset:// handle; omit when the question names the sheet"),
			}, "question"),
		},
	}
}

func object(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		values := make([]any, 0, len(required))
		for _, name := range required {
			values = append(values, name)
		}
		schema["required"] = values
	}
	return schema
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integerProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}
