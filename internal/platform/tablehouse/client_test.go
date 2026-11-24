package tablehouse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/contracts/ports"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, server
}

func TestWriteBatchSendsTargetAndClosesSheet(t *testing.T) {
	var received materializeRequest
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/materialize/rows" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "done", "rows": 42, "bytes": 1024,
			"path": "tablehouse/tenant=t/kb=kb/doc=doc/ver=v/sheet=Sheet1-abc",
		})
	})
	result, err := client.WriteBatch(context.Background(), ports.TableMaterializeTarget{
		TenantID: "t", KnowledgeBaseID: "kb", DocumentID: "doc", VersionID: "v",
	}, ports.TableRowBatch{
		Sheet: "Sheet1", Header: []string{"name"}, ColumnTypes: []string{"string"},
		Rows: [][]string{{"pen"}}, RowStart: 2, ParsedRows: 1, SheetDone: true, ContentHash: "h",
	})
	if err != nil {
		t.Fatalf("write batch: %v", err)
	}
	if received.Tenant != "t" || received.Version != "v" || received.Sheet != "Sheet1" || !received.SheetDone {
		t.Fatalf("request = %+v", received)
	}
	if received.RowStart != 2 || received.ParsedRows != 1 || len(received.Rows) != 1 {
		t.Fatalf("rows payload = %+v", received)
	}
	if result.Status != ports.MaterializeStatusReady || result.Rows != 42 || result.Bytes != 1024 || result.URI == "" || result.CompletedAt == nil {
		t.Fatalf("result = %+v", result)
	}
}

func TestWriteBatchMapsSheetRejection(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"error":"sheet row limit exceeded"}`))
	})
	_, err := client.WriteBatch(context.Background(), ports.TableMaterializeTarget{}, ports.TableRowBatch{Sheet: "S1", Header: []string{"a"}, Rows: [][]string{{"x"}}})
	var sheetErr *ports.TableSheetError
	if !errors.As(err, &sheetErr) || sheetErr.Message != "sheet row limit exceeded" {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteBatchMapsUnavailable(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"busy"}`))
	})
	_, err := client.WriteBatch(context.Background(), ports.TableMaterializeTarget{}, ports.TableRowBatch{Sheet: "S1", Header: []string{"a"}, Rows: [][]string{{"x"}}})
	if !errors.Is(err, apperror.ErrNotReady) {
		t.Fatalf("error = %v", err)
	}
}

func TestQueryTableDecodesRows(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/query" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body queryRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Limit != 10 || len(body.Filters) != 1 || body.OrderBy != "qty" || !body.Desc {
			t.Fatalf("query payload = %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"header": []string{"name", "qty"}, "totalRows": 3, "matchedRows": 2,
			"rows": []map[string]any{
				{"rowNumber": 2, "cells": map[string]string{"name": "pen", "qty": "2"}},
				{"rowNumber": 4, "cells": map[string]string{"name": "box", "qty": "3"}},
			},
		})
	})
	result, err := client.QueryTable(context.Background(), ports.TableMaterializeTarget{TenantID: "t"}, "Sheet1", ports.ColumnarTableQuery{
		Filters: []ports.TableFilter{{Column: "qty", Op: "gte", Value: "2"}}, OrderBy: "qty", Desc: true, Limit: 10,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if result.MatchedRows != 2 || len(result.Rows) != 2 || result.Rows[1].Cells["name"] != "box" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunTableSQLSendsBoundedQuery(t *testing.T) {
	var received sqlRequest
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sql" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"columns":  []map[string]string{{"name": "total", "type": "NUMBER"}},
			"rows":     [][]any{{3}},
			"rowCount": 1, "returnedRows": 1, "truncated": false, "elapsedMs": 12,
		})
	})
	result, err := client.RunTableSQL(context.Background(), ports.TableMaterializeTarget{
		TenantID: "t", KnowledgeBaseID: "kb", DocumentID: "doc", VersionID: "v",
	}, "Sheet1", ports.ColumnarSQLQuery{
		SQL: `SELECT count(*) AS total FROM t WHERE "名称" = ?`, Params: []any{"pen"}, Preview: true, Limit: 25,
	})
	if err != nil {
		t.Fatalf("run sql: %v", err)
	}
	if received.Tenant != "t" || received.Sheet != "Sheet1" || received.SQL == "" || !received.Preview || received.Limit != 25 {
		t.Fatalf("request = %+v", received)
	}
	if len(received.Params) != 1 || received.Params[0] != "pen" {
		t.Fatalf("params = %+v", received.Params)
	}
	if len(result.Columns) != 1 || result.Columns[0].Name != "total" || result.RowCount != 1 || result.ElapsedMs != 12 {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunTableSQLMapsRejection(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"only SELECT statements are allowed"}`))
	})
	_, err := client.RunTableSQL(context.Background(), ports.TableMaterializeTarget{}, "Sheet1", ports.ColumnarSQLQuery{SQL: "DELETE FROM t"})
	var sheetErr *ports.TableSheetError
	if !errors.As(err, &sheetErr) || sheetErr.Status != http.StatusBadRequest || sheetErr.Message == "" {
		t.Fatalf("error = %v", err)
	}
}

func TestClearVersionRejectsEmptyURL(t *testing.T) {
	if _, err := New("  ", time.Second); err == nil {
		t.Fatalf("empty URL must fail")
	}
}
