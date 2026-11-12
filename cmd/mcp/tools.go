package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listKnowledgeBasesInput struct{}

type listDocumentsInput struct {
	KnowledgeBaseID string `json:"knowledgeBaseId" jsonschema:"the knowledge base whose documents should be listed; must be inside the API key scope"`
	Cursor          string `json:"cursor,omitempty" jsonschema:"opaque cursor returned by a previous list_documents call"`
	Limit           int    `json:"limit,omitempty" jsonschema:"page size; the server validates the range (default 20)"`
}

type documentInput struct {
	DocumentID string `json:"documentId" jsonschema:"the document id returned by list_documents or search"`
}

type searchInput struct {
	Query           string `json:"query" jsonschema:"the question or keywords to search for"`
	KnowledgeBaseID string `json:"knowledgeBaseId" jsonschema:"the knowledge base to search; must be inside the API key scope"`
	SessionOverride string `json:"sessionOverride,omitempty" jsonschema:"optional retrieval parameter override"`
}

type askInput struct {
	KnowledgeBaseID string `json:"knowledgeBaseId,omitempty" jsonschema:"the knowledge base used to create a session when sessionId is absent"`
	Question        string `json:"question" jsonschema:"the question to answer from the knowledge base"`
	SessionID       string `json:"sessionId,omitempty" jsonschema:"reuse an existing conversation instead of creating one"`
	Title           string `json:"title,omitempty" jsonschema:"optional title when a new session is created"`
	RequestID       string `json:"requestId,omitempty" jsonschema:"optional idempotency/request id"`
}

type documentStatusInput struct {
	DocumentID string `json:"documentId" jsonschema:"the document whose processing status is requested"`
}

type citationInput struct {
	ChunkID string `json:"chunkId" jsonschema:"the chunk id returned in a search result or citation"`
}

func registerTools(server *mcp.Server, api *apiClient) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_knowledge_bases",
		Description: "List knowledge bases visible to the configured API key.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listKnowledgeBasesInput) (*mcp.CallToolResult, any, error) {
		var output any
		if err := api.doJSON(ctx, http.MethodGet, "/knowledge-bases", nil, &output); err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"knowledgeBases": output}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_documents",
		Description: "List documents in one knowledge base with their processing status and current version. Read-only and cursor-paginated.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listDocumentsInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.KnowledgeBaseID) == "" {
			return nil, nil, fmt.Errorf("knowledgeBaseId is required")
		}
		if in.Limit < 0 {
			return nil, nil, fmt.Errorf("limit must be non-negative")
		}
		query := url.Values{}
		if strings.TrimSpace(in.Cursor) != "" {
			query.Set("cursor", in.Cursor)
		}
		if in.Limit > 0 {
			query.Set("limit", strconv.Itoa(in.Limit))
		}
		path := "/knowledge-bases/" + url.PathEscape(in.KnowledgeBaseID) + "/documents"
		if len(query) > 0 {
			path += "?" + query.Encode()
		}
		var page any
		if err := api.doJSON(ctx, http.MethodGet, path, nil, &page); err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"documentPage": page}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_document",
		Description: "Read one document's metadata, current version and pipeline stages.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in documentInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.DocumentID) == "" {
			return nil, nil, fmt.Errorf("documentId is required")
		}
		var document any
		path := "/documents/" + url.PathEscape(in.DocumentID)
		if err := api.doJSON(ctx, http.MethodGet, path, nil, &document); err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"document": document}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_document_content",
		Description: "Read the parsed content preview (normalized blocks) for one document. Only available after the current version reaches EVIDENCE_READY.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in documentInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.DocumentID) == "" {
			return nil, nil, fmt.Errorf("documentId is required")
		}
		var content any
		path := "/documents/" + url.PathEscape(in.DocumentID) + "/content"
		if err := api.doJSON(ctx, http.MethodGet, path, nil, &content); err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"content": content}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "search",
		Description: "Retrieve authorized evidence chunks from one knowledge base. Returns chunk ids, document ids, quotes and source locations.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Query) == "" {
			return nil, nil, fmt.Errorf("query is required")
		}
		if strings.TrimSpace(in.KnowledgeBaseID) == "" {
			return nil, nil, fmt.Errorf("knowledgeBaseId is required")
		}
		var evidence any
		request := map[string]any{"query": in.Query, "knowledgeBaseId": in.KnowledgeBaseID}
		if strings.TrimSpace(in.SessionOverride) != "" {
			request["sessionOverride"] = in.SessionOverride
		}
		if err := api.doJSON(ctx, http.MethodPost, "/search", request, &evidence); err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"evidence": evidence}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ask",
		Description: "Ask a question against a knowledge base and return the final answer with validated citations. Creates a session when sessionId is absent.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in askInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Question) == "" {
			return nil, nil, fmt.Errorf("question is required")
		}
		if strings.TrimSpace(in.SessionID) == "" && strings.TrimSpace(in.KnowledgeBaseID) == "" {
			return nil, nil, fmt.Errorf("knowledgeBaseId is required when sessionId is absent")
		}
		result, err := api.ask(ctx, in)
		if err != nil {
			return nil, nil, err
		}
		citations := result.Citations
		if citations == nil {
			citations = []any{}
		}
		return nil, map[string]any{
			"sessionId": result.SessionID,
			"requestId": result.RequestID,
			"content":   result.Content,
			"status":    result.Status,
			"citations": citations,
		}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_document_status",
		Description: "Read a document's processing status, current version and pipeline stages.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in documentStatusInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.DocumentID) == "" {
			return nil, nil, fmt.Errorf("documentId is required")
		}
		var status any
		path := "/documents/" + url.PathEscape(in.DocumentID) + "/status"
		if err := api.doJSON(ctx, http.MethodGet, path, nil, &status); err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"documentStatus": status}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_citation",
		Description: "Read the source chunk behind a search result or answer citation. The server re-checks document authorization.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in citationInput) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.ChunkID) == "" {
			return nil, nil, fmt.Errorf("chunkId is required")
		}
		var chunk any
		path := "/chunks/" + url.PathEscape(in.ChunkID)
		if err := api.doJSON(ctx, http.MethodGet, path, nil, &chunk); err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"chunk": chunk}, nil
	})
}
