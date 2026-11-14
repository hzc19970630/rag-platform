package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"
)

type httpDocObjects struct {
	data map[string][]byte
}

func (o *httpDocObjects) Put(_ context.Context, key string, content []byte, _ string) error {
	o.data[key] = append([]byte(nil), content...)
	return nil
}

func (o *httpDocObjects) Get(_ context.Context, key string) ([]byte, error) {
	content, ok := o.data[key]
	if !ok {
		return nil, io.EOF
	}
	return content, nil
}

func (o *httpDocObjects) Stat(_ context.Context, key string) (int64, error) {
	content, ok := o.data[key]
	if !ok {
		return 0, io.EOF
	}
	return int64(len(content)), nil
}

func (o *httpDocObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	content, ok := o.data[key]
	if !ok {
		return nil, io.EOF
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (o *httpDocObjects) Delete(_ context.Context, key string) error {
	delete(o.data, key)
	return nil
}

func TestDocumentRoutes(t *testing.T) {
	router, _ := newProjectionHTTPRouter(t)
	cases := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/api/v1/knowledge-bases/kb-a/documents", http.StatusOK},
		{http.MethodGet, "/api/v1/documents/doc-a", http.StatusOK},
		{http.MethodGet, "/api/v1/documents/doc-a/status", http.StatusOK},
		{http.MethodGet, "/api/v1/documents/doc-a/content", http.StatusOK},
		{http.MethodGet, "/api/v1/documents/doc-a/chunks", http.StatusOK},
		{http.MethodGet, "/api/v1/chunks/chunk-a", http.StatusOK},
		{http.MethodGet, "/api/v1/documents/doc-a/versions", http.StatusOK},
		{http.MethodGet, "/api/v1/documents/doc-a/preview", http.StatusOK},
		{http.MethodGet, "/api/v1/documents/missing/status", http.StatusNotFound},
		{http.MethodGet, "/api/v1/chunks/missing", http.StatusNotFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			recorder := projectionRequest(t, router, testCase.method, testCase.path, "")
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, testCase.want, recorder.Body.String())
			}
		})
	}
}

func TestDocumentUploadReparseActivateRestorePurge(t *testing.T) {
	router, _ := newProjectionHTTPRouter(t)

	upload := multipartDocumentRequest(t, router, "/api/v1/knowledge-bases/kb-a/documents", "upload.txt", []byte("uploaded body"))
	if upload.Code != http.StatusAccepted {
		t.Fatalf("upload status = %d, body = %s", upload.Code, upload.Body.String())
	}

	reparse := projectionRequest(t, router, http.MethodPost, "/api/v1/documents/doc-a/reparse", "")
	if reparse.Code != http.StatusAccepted {
		t.Fatalf("reparse status = %d, body = %s", reparse.Code, reparse.Body.String())
	}

	activate := projectionRequest(t, router, http.MethodPost, "/api/v1/documents/doc-a/versions/ver-b/activate", "")
	if activate.Code != http.StatusOK {
		t.Fatalf("activate status = %d, body = %s", activate.Code, activate.Body.String())
	}

	if recorder := projectionRequest(t, router, http.MethodDelete, "/api/v1/documents/doc-a", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/documents/doc-a/restore", ""); recorder.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := projectionRequest(t, router, http.MethodDelete, "/api/v1/documents/doc-a", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("second delete status = %d", recorder.Code)
	}
	if recorder := projectionRequest(t, router, http.MethodPost, "/api/v1/documents/doc-a/purge", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("purge status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestDocumentUploadRejectsBadPayloads(t *testing.T) {
	router, _ := newProjectionHTTPRouter(t)
	missingFile := projectionRequest(t, router, http.MethodPost, "/api/v1/knowledge-bases/kb-a/documents", "")
	if missingFile.Code == http.StatusAccepted {
		t.Fatalf("upload without a file must fail, got %d", missingFile.Code)
	}
	unsupported := multipartDocumentRequest(t, router, "/api/v1/knowledge-bases/kb-a/documents", "upload.exe", []byte("x"))
	if unsupported.Code != http.StatusBadRequest {
		t.Fatalf("unsupported upload status = %d, body = %s", unsupported.Code, unsupported.Body.String())
	}
}

func multipartDocumentRequest(t *testing.T, router http.Handler, path, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	header.Set("Content-Type", "text/plain")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-Dev-User-ID", "owner-a")
	request.Header.Set("X-Dev-Tenant-ID", "tenant-a")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
