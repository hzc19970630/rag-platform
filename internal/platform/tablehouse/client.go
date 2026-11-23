// Package tablehouse talks to the columnar table service. It only receives
// server-built identifiers and filter plans; user SQL never leaves the API.
package tablehouse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/ports"
)

type Client struct {
	base         string
	http         *http.Client
	queryTimeout time.Duration
	writeTimeout time.Duration
}

// New builds a client. queryTimeout bounds /query; materialization and prefix
// clearing get a separate, longer deadline because a 100k-row Parquet flush and
// a recursive delete can far exceed a query budget.
func New(baseURL string, queryTimeout time.Duration) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("tablehouse URL is empty")
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("tablehouse URL: %w", err)
	}
	if queryTimeout <= 0 {
		queryTimeout = 15 * time.Second
	}
	writeTimeout := 2 * time.Minute
	if queryTimeout > writeTimeout {
		writeTimeout = queryTimeout
	}
	return &Client{base: base, http: &http.Client{}, queryTimeout: queryTimeout, writeTimeout: writeTimeout}, nil
}

type targetPayload struct {
	Tenant        string `json:"tenant"`
	KnowledgeBase string `json:"knowledge_base"`
	Document      string `json:"document"`
	Version       string `json:"version"`
}

func payload(target ports.TableMaterializeTarget) targetPayload {
	return targetPayload{
		Tenant: target.TenantID, KnowledgeBase: target.KnowledgeBaseID,
		Document: target.DocumentID, Version: target.VersionID,
	}
}

func (c *Client) ClearVersion(ctx context.Context, target ports.TableMaterializeTarget) error {
	body, err := json.Marshal(payload(target))
	if err != nil {
		return err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, c.writeTimeout)
	defer cancel()
	_, err = c.post(timeoutCtx, "/materialize/version/clear", body)
	return err
}

type materializeRequest struct {
	targetPayload
	Sheet       string     `json:"sheet"`
	Header      []string   `json:"header"`
	ColumnTypes []string   `json:"column_types,omitempty"`
	RowStart    int64      `json:"row_start"`
	Rows        [][]string `json:"rows"`
	ParsedRows  int64      `json:"parsed_rows"`
	SheetDone   bool       `json:"sheet_done"`
	Truncated   bool       `json:"truncated"`
	ContentHash string     `json:"content_hash,omitempty"`
}

type materializeResponse struct {
	Status        string `json:"status"`
	Rows          int64  `json:"rows"`
	Bytes         int64  `json:"bytes"`
	Path          string `json:"path"`
	ManifestBytes int64  `json:"manifestBytes"`
}

func (c *Client) WriteBatch(ctx context.Context, target ports.TableMaterializeTarget, batch ports.TableRowBatch) (ports.TableMaterializeResult, error) {
	body, err := json.Marshal(materializeRequest{
		targetPayload: payload(target),
		Sheet:         batch.Sheet,
		Header:        batch.Header,
		ColumnTypes:   batch.ColumnTypes,
		RowStart:      batch.RowStart,
		Rows:          batch.Rows,
		ParsedRows:    batch.ParsedRows,
		SheetDone:     batch.SheetDone,
		Truncated:     batch.Truncated,
		ContentHash:   batch.ContentHash,
	})
	if err != nil {
		return ports.TableMaterializeResult{}, err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, c.writeTimeout)
	defer cancel()
	response, err := c.post(timeoutCtx, "/materialize/rows", body)
	if err != nil {
		return ports.TableMaterializeResult{}, err
	}
	var decoded materializeResponse
	if err := json.Unmarshal(response, &decoded); err != nil {
		return ports.TableMaterializeResult{}, fmt.Errorf("decode tablehouse response: %w", err)
	}
	result := ports.TableMaterializeResult{
		Rows: decoded.Rows, Bytes: decoded.Bytes, URI: decoded.Path,
		ColumnTypes: batch.ColumnTypes, ContentHash: batch.ContentHash,
	}
	if batch.SheetDone {
		result.Status = ports.MaterializeStatusReady
		if batch.Truncated {
			result.Status = ports.MaterializeStatusPartial
		}
		now := time.Now().UTC()
		result.CompletedAt = &now
	}
	return result, nil
}

type queryRequest struct {
	targetPayload
	Sheet   string              `json:"sheet"`
	Filters []ports.TableFilter `json:"filters,omitempty"`
	OrderBy string              `json:"order_by,omitempty"`
	Desc    bool                `json:"desc,omitempty"`
	Limit   int                 `json:"limit"`
}

type queryResponse struct {
	Header      []string             `json:"header"`
	TotalRows   int                  `json:"totalRows"`
	MatchedRows int                  `json:"matchedRows"`
	Rows        []ports.TableRowView `json:"rows"`
}

func (c *Client) QueryTable(ctx context.Context, target ports.TableMaterializeTarget, sheet string, query ports.ColumnarTableQuery) (ports.ColumnarTableResult, error) {
	body, err := json.Marshal(queryRequest{
		targetPayload: payload(target),
		Sheet:         sheet,
		Filters:       append([]ports.TableFilter(nil), query.Filters...),
		OrderBy:       query.OrderBy,
		Desc:          query.Desc,
		Limit:         query.Limit,
	})
	if err != nil {
		return ports.ColumnarTableResult{}, err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, c.queryTimeout)
	defer cancel()
	response, err := c.post(timeoutCtx, "/query", body)
	if err != nil {
		return ports.ColumnarTableResult{}, err
	}
	var decoded queryResponse
	if err := json.Unmarshal(response, &decoded); err != nil {
		return ports.ColumnarTableResult{}, fmt.Errorf("decode tablehouse query: %w", err)
	}
	return ports.ColumnarTableResult{Rows: decoded.Rows, MatchedRows: decoded.MatchedRows, TotalRows: decoded.TotalRows}, nil
}

func (c *Client) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: tablehouse unavailable: %v", apperror.ErrNotReady, err)
	}
	defer response.Body.Close()
	content, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if readErr != nil {
		return nil, fmt.Errorf("%w: tablehouse response: %v", apperror.ErrNotReady, readErr)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return content, nil
	}
	var failure struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(content, &failure)
	message := failure.Error
	if message == "" {
		message = strings.TrimSpace(string(content))
	}
	switch response.StatusCode {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return nil, &ports.TableSheetError{Status: response.StatusCode, Message: message}
	case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return nil, fmt.Errorf("%w: %s", apperror.ErrNotReady, message)
	default:
		return nil, fmt.Errorf("tablehouse error %d: %s", response.StatusCode, message)
	}
}
