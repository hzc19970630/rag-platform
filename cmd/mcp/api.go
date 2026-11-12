package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// apiClient is a thin HTTP client for the platform API. Every call carries the
// caller's API key; the server resolves tenant and knowledge-base scope, so the
// MCP server never bypasses authorization.
type apiClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("rag api %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("rag api %d %s", e.Status, e.Code)
}

func newAPIClient(baseURL, apiKey string, timeout time.Duration) *apiClient {
	return &apiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *apiClient) request(ctx context.Context, method, path string, body any, accept string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", path, err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	var payload struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&payload)
	return nil, &apiError{Status: resp.StatusCode, Code: payload.Error, Message: payload.Message}
}

func (c *apiClient) doJSON(ctx context.Context, method, path string, in, out any) error {
	resp, err := c.request(ctx, method, path, in, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

type streamEvent struct {
	RequestID string         `json:"request_id"`
	Sequence  int            `json:"sequence"`
	Type      string         `json:"type"`
	Data      map[string]any `json:"data"`
}

// streamEvents reads the SSE frames emitted by the answer endpoints. The
// platform writes `event:`, `id:` and `data:` lines; only the data payload is
// needed because it repeats the type and sequence.
func (c *apiClient) streamEvents(ctx context.Context, method, path string, in any, handle func(streamEvent) error) error {
	resp, err := c.request(ctx, method, path, in, "text/event-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		return fmt.Errorf("expected an event stream, got %q", contentType)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) == 0 {
				continue
			}
			payload := strings.Join(data, "\n")
			data = data[:0]
			var event streamEvent
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				return fmt.Errorf("decode stream event: %w", err)
			}
			if err := handle(event); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(data) > 0 {
		var event streamEvent
		if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &event); err != nil {
			return fmt.Errorf("decode trailing stream event: %w", err)
		}
		if err := handle(event); err != nil {
			return err
		}
	}
	return nil
}

type askResult struct {
	SessionID string
	RequestID string
	Content   string
	Status    string
	Code      string
	Citations any
}

func (c *apiClient) ask(ctx context.Context, in askInput) (askResult, error) {
	result := askResult{Citations: []any{}}
	sessionID := strings.TrimSpace(in.SessionID)
	if sessionID == "" {
		var conversation struct {
			ID string `json:"id"`
		}
		request := map[string]any{"knowledge_base_id": in.KnowledgeBaseID}
		if strings.TrimSpace(in.Title) != "" {
			request["title"] = in.Title
		}
		if err := c.doJSON(ctx, http.MethodPost, "/sessions", request, &conversation); err != nil {
			return result, err
		}
		if conversation.ID == "" {
			return result, fmt.Errorf("session creation returned no id")
		}
		sessionID = conversation.ID
	}
	requestID := strings.TrimSpace(in.RequestID)
	if requestID == "" {
		requestID = newRequestID()
	}
	result.SessionID = sessionID
	result.RequestID = requestID

	lastSequence := 0
	complete := false
	tailTimeout := false
	handle := func(event streamEvent) error {
		if event.Sequence > lastSequence {
			lastSequence = event.Sequence
		}
		switch event.Type {
		case "answer":
			if content, ok := event.Data["content"].(string); ok {
				result.Content = content
			}
			if citations, ok := event.Data["citations"]; ok && citations != nil {
				result.Citations = citations
			}
			if status, ok := event.Data["status"].(string); ok {
				result.Status = status
			}
		case "complete":
			if status, ok := event.Data["status"].(string); ok {
				result.Status = status
			}
			if code, ok := event.Data["code"].(string); ok {
				result.Code = code
			}
			complete = true
		case "error":
			code, _ := event.Data["code"].(string)
			message, _ := event.Data["message"].(string)
			if code == "tail_timeout" {
				tailTimeout = true
				return nil
			}
			return &apiError{Code: code, Message: message}
		}
		return nil
	}

	path := "/sessions/" + url.PathEscape(sessionID) + "/messages"
	if err := c.streamEvents(ctx, http.MethodPost, path, map[string]any{"query": in.Question, "requestId": requestID}, handle); err != nil {
		return result, err
	}
	for attempt := 0; attempt < 3 && !complete; attempt++ {
		if !tailTimeout && lastSequence == 0 {
			break
		}
		tailTimeout = false
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 250 * time.Millisecond):
		}
		eventsPath := fmt.Sprintf("/requests/%s/events?stream=1&after=%d", url.PathEscape(requestID), lastSequence)
		if err := c.streamEvents(ctx, http.MethodGet, eventsPath, nil, handle); err != nil {
			return result, err
		}
	}
	if !complete {
		return result, fmt.Errorf("answer stream ended before a terminal event (request %s)", requestID)
	}
	switch result.Status {
	case "FAILED", "TIMEOUT", "CANCELLED":
		return result, &apiError{Code: result.Code, Message: "answer finished with status " + result.Status}
	}
	return result, nil
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("mcp-%d", time.Now().UnixNano())
	}
	return "mcp-" + hex.EncodeToString(raw[:])
}
