//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func callToolE2E(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var output map[string]any
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			continue
		}
		_ = json.Unmarshal([]byte(text.Text), &output)
	}
	return result, output
}

// TestQuickstartE2E runs the MCP client against a live quickstart stack. It is
// opt-in because it needs the platform, a seeded knowledge base and an API key.
func TestQuickstartE2E(t *testing.T) {
	if os.Getenv("RAG_E2E") != "1" {
		t.Skip("set RAG_E2E=1 and RAG_API_KEY to run the live MCP test")
	}
	apiURL := os.Getenv("RAG_API_URL")
	apiKey := os.Getenv("RAG_API_KEY")
	knowledgeBaseID := os.Getenv("RAG_KNOWLEDGE_BASE_ID")
	forbiddenKB := os.Getenv("RAG_FORBIDDEN_KB")
	if apiURL == "" || apiKey == "" || knowledgeBaseID == "" || forbiddenKB == "" {
		t.Fatal("RAG_API_URL, RAG_API_KEY, RAG_KNOWLEDGE_BASE_ID and RAG_FORBIDDEN_KB are required")
	}
	session := connectTestClient(t, newAPIClient(apiURL, apiKey, 240*time.Second))

	result, output := callToolE2E(t, session, "list_knowledge_bases", map[string]any{})
	if result.IsError {
		t.Fatalf("list_knowledge_bases failed")
	}
	if output["knowledgeBases"] == nil {
		t.Fatalf("list_knowledge_bases returned no payload")
	}

	result, output = callToolE2E(t, session, "list_documents", map[string]any{"knowledgeBaseId": knowledgeBaseID})
	if result.IsError {
		t.Fatalf("list_documents failed")
	}
	page, _ := output["documentPage"].(map[string]any)
	documentItems, _ := page["items"].([]any)
	if len(documentItems) == 0 {
		t.Fatalf("list_documents returned no documents; upload examples/demo/retrieval-handbook.md to %s and rerun", knowledgeBaseID)
	}
	firstDocument, _ := documentItems[0].(map[string]any)
	documentSummary, _ := firstDocument["document"].(map[string]any)
	documentID, _ := documentSummary["id"].(string)
	if documentID == "" {
		t.Fatalf("list_documents returned no document id: %v", firstDocument)
	}

	result, output = callToolE2E(t, session, "get_document", map[string]any{"documentId": documentID})
	if result.IsError {
		t.Fatalf("get_document failed")
	}
	detail, _ := output["document"].(map[string]any)
	document, _ := detail["document"].(map[string]any)
	if document["filename"] == "" {
		t.Fatalf("get_document returned no filename: %v", detail)
	}

	result, output = callToolE2E(t, session, "get_document_content", map[string]any{"documentId": documentID})
	if result.IsError {
		t.Fatalf("get_document_content failed")
	}
	content, _ := output["content"].(map[string]any)
	blocks, _ := content["blocks"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("get_document_content returned no parsed blocks: %v", content)
	}

	result, output = callToolE2E(t, session, "search", map[string]any{
		"query":           "hybrid recall",
		"knowledgeBaseId": knowledgeBaseID,
	})
	if result.IsError {
		t.Fatalf("search failed")
	}
	evidence, _ := output["evidence"].(map[string]any)
	items, _ := evidence["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("search returned no evidence; upload examples/demo/retrieval-handbook.md to %s and rerun", knowledgeBaseID)
	}

	result, output = callToolE2E(t, session, "ask", map[string]any{
		"knowledgeBaseId": knowledgeBaseID,
		"question":        "What is hybrid recall?",
	})
	if result.IsError {
		t.Fatalf("ask failed")
	}
	if output["content"] == "" {
		t.Fatalf("ask returned empty content: %v", output)
	}
	citations, _ := output["citations"].([]any)
	if len(citations) == 0 {
		t.Fatalf("ask returned no citations (status=%v)", output["status"])
	}
	first, _ := citations[0].(map[string]any)
	chunkID, _ := first["chunkId"].(string)
	if chunkID == "" {
		t.Fatalf("first citation has no chunkId: %v", first)
	}

	result, output = callToolE2E(t, session, "get_citation", map[string]any{"chunkId": chunkID})
	if result.IsError {
		t.Fatalf("get_citation failed")
	}
	chunk, _ := output["chunk"].(map[string]any)
	if chunk["content"] == "" {
		t.Fatalf("citation chunk has no content: %v", chunk)
	}

	result, _ = callToolE2E(t, session, "search", map[string]any{
		"query":           "hybrid recall",
		"knowledgeBaseId": forbiddenKB,
	})
	if !result.IsError {
		t.Fatalf("out-of-scope search must be a tool error")
	}

	result, _ = callToolE2E(t, session, "list_documents", map[string]any{
		"knowledgeBaseId": forbiddenKB,
	})
	if !result.IsError {
		t.Fatalf("out-of-scope list_documents must be a tool error")
	}
}
