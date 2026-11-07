package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/resilience"
)

const chatSuccessBody = `{"model":"test","choices":[{"message":{"role":"assistant","content":"answer"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`

func TestRemoteChatterRetriesTransientProviderFailure(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			http.Error(writer, "temporary", http.StatusBadGateway)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(chatSuccessBody))
	}))
	defer server.Close()
	guard := resilience.New(resilience.Config{MaxAttempts: 3, BaseBackoff: time.Millisecond})
	chatter, err := NewRemoteChatter(RemoteChatterOptions{BaseURL: server.URL, Model: "test", Timeout: time.Second, Guard: guard})
	if err != nil {
		t.Fatalf("construct adapter: %v", err)
	}
	response, err := chatter.Complete(context.Background(), model.ChatRequest{Query: "hello"})
	if err != nil {
		t.Fatalf("complete after retry: %v", err)
	}
	if response.Text != "answer" || calls != 3 {
		t.Fatalf("response=%q calls=%d, want answer after 3 calls", response.Text, calls)
	}
}

func TestRemoteChatterStopsAfterBreakerOpens(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(writer, "down", http.StatusInternalServerError)
	}))
	defer server.Close()
	guard := resilience.New(resilience.Config{FailureThreshold: 1, Cooldown: time.Minute, MaxAttempts: 1})
	chatter, err := NewRemoteChatter(RemoteChatterOptions{BaseURL: server.URL, Model: "test", Timeout: time.Second, Guard: guard})
	if err != nil {
		t.Fatalf("construct adapter: %v", err)
	}
	if _, err := chatter.Complete(context.Background(), model.ChatRequest{Query: "hello"}); err == nil {
		t.Fatal("expected the first provider call to fail")
	}
	if _, err := chatter.Complete(context.Background(), model.ChatRequest{Query: "hello"}); !errors.Is(err, resilience.ErrCircuitOpen) {
		t.Fatalf("second call = %v, want ErrCircuitOpen", err)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1 while the breaker is open", calls)
	}
}
