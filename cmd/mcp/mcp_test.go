package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type requestLog struct {
	mu       sync.Mutex
	requests []string
	auth     []string
}

func (l *requestLog) record(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, r.Method+" "+r.URL.Path)
	l.auth = append(l.auth, r.Header.Get("Authorization"))
}

func (l *requestLog) has(entry string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, seen := range l.requests {
		if seen == entry {
			return true
		}
	}
	return false
}

func newTestAPI(t *testing.T) (*httptest.Server, *requestLog) {
	t.Helper()
	log := &requestLog{}
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		log.record(r)
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"unauthenticated","message":"bad key"}`)
			return false
		}
		return true
	}
	mux.HandleFunc("/api/v1/knowledge-bases", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[{"id":"kb1","name":"KB One"}]}`)
	})
	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		var body struct {
			KnowledgeBaseID string `json:"knowledgeBaseId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if body.KnowledgeBaseID == "kb-forbidden" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":"forbidden","message":"knowledge base is outside the API key scope"}`)
			return
		}
		fmt.Fprint(w, `{"schemaVersion":"EvidenceSetV1","query":"q","items":[{"chunkId":"c1","documentId":"d1","knowledgeBaseId":"kb1","content":"hello","headingPath":"H","sourcePath":"doc.md"}]}`)
	})
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"s1","knowledgeBaseId":"kb1"}`)
	})
	mux.HandleFunc("/api/v1/sessions/s1/messages", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse("answer", "req-1:1", `{"request_id":"req-1","sequence":1,"type":"answer","data":{"content":"answer text","citations":[{"chunkId":"c1"}],"status":"COMPLETED"}}`))
		fmt.Fprint(w, sse("complete", "req-1:2", `{"request_id":"req-1","sequence":2,"type":"complete","data":{"status":"COMPLETED"}}`))
	})
	mux.HandleFunc("/api/v1/requests/req-1/events", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Query().Get("after") == "1" {
			fmt.Fprint(w, sse("answer", "req-1:1", `{"request_id":"req-1","sequence":1,"type":"answer","data":{"content":"resumed answer","citations":[],"status":"COMPLETED"}}`))
			fmt.Fprint(w, sse("complete", "req-1:2", `{"request_id":"req-1","sequence":2,"type":"complete","data":{"status":"COMPLETED"}}`))
			return
		}
		fmt.Fprint(w, sse("complete", "req-1:2", `{"request_id":"req-1","sequence":2,"type":"complete","data":{"status":"COMPLETED"}}`))
	})
	mux.HandleFunc("/api/v1/chunks/c1", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"chunkId":"c1","documentId":"d1","content":"hello","headingPath":"H","sourcePath":"doc.md"}`)
	})
	mux.HandleFunc("/api/v1/documents/d1/status", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"document":{"id":"d1","status":"EVIDENCE_READY"},"contentReady":true}`)
	})
	mux.HandleFunc("/api/v1/knowledge-bases/kb1/documents", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"schemaVersion":"DocumentPageV1","knowledgeBaseId":"kb1","items":[{"document":{"id":"d1","knowledgeBaseId":"kb1","filename":"doc.md","mimeType":"text/markdown","status":"EVIDENCE_READY"},"version":{"id":"v1","version":1,"status":"EVIDENCE_READY"},"contentReady":true,"previewable":true}],"nextCursor":"next","echoCursor":%q,"echoLimit":%q}`,
			r.URL.Query().Get("cursor"), r.URL.Query().Get("limit"))
	})
	mux.HandleFunc("/api/v1/knowledge-bases/kb-forbidden/documents", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":"forbidden","message":"knowledge base is outside the API key scope"}`)
	})
	mux.HandleFunc("/api/v1/documents/d1", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"document":{"id":"d1","knowledgeBaseId":"kb1","filename":"doc.md","status":"EVIDENCE_READY"},"version":{"id":"v1","version":1,"status":"EVIDENCE_READY"},"pipeline":{"status":"EVIDENCE_READY"},"stages":[]}`)
	})
	mux.HandleFunc("/api/v1/documents/d1/content", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"schemaVersion":"DocumentPreviewV1","documentId":"d1","versionId":"v1","knowledgeBaseId":"kb1","filename":"doc.md","status":"EVIDENCE_READY","blocks":[{"blockId":"b1","kind":"paragraph","text":"hello","headingPath":"H","pageNumber":1,"sourcePath":"doc.md"}]}`)
	})
	return httptest.NewServer(mux), log
}

func sse(eventType, id, data string) string {
	return fmt.Sprintf("event: %s\nid: %s\ndata: %s\n\n", eventType, id, data)
}

func connectTestClient(t *testing.T, api *apiClient) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	registerTools(server, api)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() {
		_ = server.Run(ctx, serverTransport)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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

func TestSearchForwardsAPIKeyAndReturnsEvidence(t *testing.T) {
	server, log := newTestAPI(t)
	defer server.Close()
	session := connectTestClient(t, newAPIClient(server.URL+"/api/v1", "test-key", 10*time.Second))
	result, output := callTool(t, session, "search", map[string]any{"query": "hello", "knowledgeBaseId": "kb1"})
	if result.IsError {
		t.Fatalf("search returned tool error")
	}
	evidence, _ := output["evidence"].(map[string]any)
	items, _ := evidence["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", evidence["items"])
	}
	if log.auth[0] != "Bearer test-key" {
		t.Fatalf("authorization header = %q", log.auth[0])
	}
}

func TestSearchRejectsOutOfScopeKnowledgeBase(t *testing.T) {
	server, _ := newTestAPI(t)
	defer server.Close()
	session := connectTestClient(t, newAPIClient(server.URL+"/api/v1", "test-key", 10*time.Second))
	result, _ := callTool(t, session, "search", map[string]any{"query": "hello", "knowledgeBaseId": "kb-forbidden"})
	if !result.IsError {
		t.Fatalf("out-of-scope search must be a tool error")
	}
}

func TestAskStreamsAnswerAndCitations(t *testing.T) {
	server, log := newTestAPI(t)
	defer server.Close()
	session := connectTestClient(t, newAPIClient(server.URL+"/api/v1", "test-key", 10*time.Second))
	result, output := callTool(t, session, "ask", map[string]any{"sessionId": "s1", "question": "hello", "requestId": "req-1"})
	if result.IsError {
		t.Fatalf("ask returned tool error")
	}
	if output["content"] != "answer text" || output["status"] != "COMPLETED" {
		t.Fatalf("ask output = %v", output)
	}
	citations, _ := output["citations"].([]any)
	if len(citations) != 1 {
		t.Fatalf("citations = %v", output["citations"])
	}
	if log.has("POST /api/v1/sessions") {
		t.Fatalf("ask with sessionId must not create a new session")
	}
}

func TestAskCreatesSessionWhenAbsent(t *testing.T) {
	server, log := newTestAPI(t)
	defer server.Close()
	session := connectTestClient(t, newAPIClient(server.URL+"/api/v1", "test-key", 10*time.Second))
	result, output := callTool(t, session, "ask", map[string]any{"knowledgeBaseId": "kb1", "question": "hello", "requestId": "req-1"})
	if result.IsError {
		t.Fatalf("ask returned tool error")
	}
	if output["sessionId"] != "s1" {
		t.Fatalf("sessionId = %v", output["sessionId"])
	}
	if !log.has("POST /api/v1/sessions") {
		t.Fatalf("expected session creation")
	}
}

func TestGetCitationAndDocumentStatus(t *testing.T) {
	server, _ := newTestAPI(t)
	defer server.Close()
	session := connectTestClient(t, newAPIClient(server.URL+"/api/v1", "test-key", 10*time.Second))
	result, output := callTool(t, session, "get_citation", map[string]any{"chunkId": "c1"})
	if result.IsError {
		t.Fatalf("citation returned tool error")
	}
	chunk, _ := output["chunk"].(map[string]any)
	if chunk["documentId"] != "d1" || chunk["content"] != "hello" {
		t.Fatalf("chunk = %v", chunk)
	}
	result, output = callTool(t, session, "get_document_status", map[string]any{"documentId": "d1"})
	if result.IsError {
		t.Fatalf("status returned tool error")
	}
	status, _ := output["documentStatus"].(map[string]any)
	if status["contentReady"] != true {
		t.Fatalf("status = %v", status)
	}
}

func TestListKnowledgeBases(t *testing.T) {
	server, _ := newTestAPI(t)
	defer server.Close()
	session := connectTestClient(t, newAPIClient(server.URL+"/api/v1", "test-key", 10*time.Second))
	result, output := callTool(t, session, "list_knowledge_bases", map[string]any{})
	if result.IsError {
		t.Fatalf("list returned tool error")
	}
	list, _ := output["knowledgeBases"].(map[string]any)
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("knowledge bases = %v", list)
	}
}

func TestListDocumentsForwardsPaginationAndScope(t *testing.T) {
	server, log := newTestAPI(t)
	defer server.Close()
	session := connectTestClient(t, newAPIClient(server.URL+"/api/v1", "test-key", 10*time.Second))
	result, output := callTool(t, session, "list_documents", map[string]any{
		"knowledgeBaseId": "kb1",
		"cursor":          "next",
		"limit":           5,
	})
	if result.IsError {
		t.Fatalf("list_documents returned tool error")
	}
	page, _ := output["documentPage"].(map[string]any)
	items, _ := page["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("documents = %v", page)
	}
	if page["echoCursor"] != "next" || page["echoLimit"] != "5" {
		t.Fatalf("pagination was not forwarded: cursor=%v limit=%v", page["echoCursor"], page["echoLimit"])
	}
	if log.auth[0] != "Bearer test-key" {
		t.Fatalf("authorization header = %q", log.auth[0])
	}
}

func TestListDocumentsRejectsOutOfScopeKnowledgeBase(t *testing.T) {
	server, _ := newTestAPI(t)
	defer server.Close()
	session := connectTestClient(t, newAPIClient(server.URL+"/api/v1", "test-key", 10*time.Second))
	result, _ := callTool(t, session, "list_documents", map[string]any{"knowledgeBaseId": "kb-forbidden"})
	if !result.IsError {
		t.Fatalf("out-of-scope list_documents must be a tool error")
	}
}

func TestGetDocumentAndContent(t *testing.T) {
	server, _ := newTestAPI(t)
	defer server.Close()
	session := connectTestClient(t, newAPIClient(server.URL+"/api/v1", "test-key", 10*time.Second))

	result, output := callTool(t, session, "get_document", map[string]any{"documentId": "d1"})
	if result.IsError {
		t.Fatalf("get_document returned tool error")
	}
	detail, _ := output["document"].(map[string]any)
	document, _ := detail["document"].(map[string]any)
	if document["filename"] != "doc.md" {
		t.Fatalf("document = %v", detail)
	}

	result, output = callTool(t, session, "get_document_content", map[string]any{"documentId": "d1"})
	if result.IsError {
		t.Fatalf("get_document_content returned tool error")
	}
	content, _ := output["content"].(map[string]any)
	blocks, _ := content["blocks"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("content blocks = %v", content)
	}
}

func TestDocumentToolsRequireIDs(t *testing.T) {
	server, _ := newTestAPI(t)
	defer server.Close()
	session := connectTestClient(t, newAPIClient(server.URL+"/api/v1", "test-key", 10*time.Second))
	for _, tool := range []string{"list_documents", "get_document", "get_document_content"} {
		result, _ := callTool(t, session, tool, map[string]any{})
		if !result.IsError {
			t.Fatalf("%s with no id must be a tool error", tool)
		}
	}
}
