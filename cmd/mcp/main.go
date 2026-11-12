// Package main runs a stdio Model Context Protocol server that exposes the
// platform's authorized retrieval, answer, document-status and citation
// capabilities. It authenticates with an API key and never opens a second
// authorization path: every tool call is a normal HTTP call with that key.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/buildinfo"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type config struct {
	apiURL  string
	apiKey  string
	timeout time.Duration
}

func main() {
	logger := log.New(os.Stderr, "rag-mcp ", log.LstdFlags)
	cfg, err := loadConfig()
	if err != nil {
		logger.Fatal(err)
	}
	client := newAPIClient(cfg.apiURL, cfg.apiKey, cfg.timeout)
	server := mcp.NewServer(&mcp.Implementation{Name: "rag-platform", Version: buildinfo.Version}, nil)
	registerTools(server, client)
	logger.Printf("starting stdio server api=%s version=%s", cfg.apiURL, buildinfo.Version)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		logger.Fatalf("mcp server: %v", err)
	}
}

func loadConfig() (config, error) {
	apiURL := strings.TrimSpace(os.Getenv("RAG_API_URL"))
	if apiURL == "" {
		apiURL = "http://localhost:8082/api/v1"
	}
	apiURL = strings.TrimRight(apiURL, "/")
	if !strings.HasSuffix(apiURL, "/api/v1") {
		apiURL += "/api/v1"
	}
	apiKey := strings.TrimSpace(os.Getenv("RAG_API_KEY"))
	if apiKey == "" {
		return config{}, errors.New("RAG_API_KEY is required; create an API key with chat and knowledge_base:read capabilities")
	}
	timeout := 180 * time.Second
	if raw := strings.TrimSpace(os.Getenv("RAG_MCP_TIMEOUT_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return config{}, errors.New("RAG_MCP_TIMEOUT_SECONDS must be a positive integer")
		}
		timeout = time.Duration(seconds) * time.Second
	}
	return config{apiURL: apiURL, apiKey: apiKey, timeout: timeout}, nil
}
