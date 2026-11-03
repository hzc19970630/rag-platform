package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/example/rag-platform/internal/ingestion"
	"github.com/gin-gonic/gin"
)

func TestPreviewDispositionNeverInlinesActiveContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := map[string]string{
		"application/pdf":       "inline",
		"text/markdown":         "inline",
		"text/plain":            "inline",
		"text/html":             "attachment",
		"application/xhtml+xml": "attachment",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "attachment",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": "attachment",
		"image/svg+xml": "attachment",
	}
	for mimeType, want := range cases {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		writeSourcePreview(context, ingestion.SourceDocument{Filename: "sample.bin", MimeType: mimeType, Content: []byte("preview")})
		if got := recorder.Header().Get("Content-Disposition"); !strings.HasPrefix(got, want+";") {
			t.Fatalf("%s: Content-Disposition = %q, want prefix %q", mimeType, got, want+";")
		}
		if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s: X-Content-Type-Options = %q", mimeType, got)
		}
		if got := recorder.Header().Get("Content-Type"); got != mimeType {
			t.Fatalf("%s: Content-Type = %q", mimeType, got)
		}
		if recorder.Code != 200 || recorder.Body.String() != "preview" {
			t.Fatalf("%s: code=%d body=%q", mimeType, recorder.Code, recorder.Body.String())
		}
	}
}
