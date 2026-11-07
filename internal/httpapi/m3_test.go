package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
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
		writeSourcePreview(context, ingestion.SourceDocument{
			Filename: "sample.bin", MimeType: mimeType, Size: int64(len("preview")),
			Body: io.NopCloser(strings.NewReader("preview")),
		})
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

type trackingReadCloser struct {
	reader io.Reader
	closed bool
}

func (t *trackingReadCloser) Read(p []byte) (int, error) { return t.reader.Read(p) }
func (t *trackingReadCloser) Close() error               { t.closed = true; return nil }

func TestPreviewStreamsAndClosesBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	content := "streamed preview"
	body := &trackingReadCloser{reader: strings.NewReader(content)}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)

	writeSourcePreview(context, ingestion.SourceDocument{
		Filename: "sample.txt", MimeType: "text/plain", Size: int64(len(content)), Body: body,
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Body.String(); got != content {
		t.Fatalf("body = %q, want %q", got, content)
	}
	if !body.closed {
		t.Fatal("preview did not close the source body")
	}
	if got, want := recorder.Header().Get("Content-Length"), strconv.Itoa(len(content)); got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}
}
