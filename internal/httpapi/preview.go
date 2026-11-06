package httpapi

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/example/rag-platform/internal/ingestion"
	"github.com/gin-gonic/gin"
)

// writeSourcePreview streams the original file without ever exposing the object
// storage location. Active content types are never served inline.
func writeSourcePreview(c *gin.Context, source ingestion.SourceDocument) {
	if source.Body == nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	defer source.Body.Close()
	disposition := "attachment"
	switch source.MimeType {
	case "application/pdf", "text/markdown", "text/plain":
		disposition = "inline"
	}
	c.Header("Content-Type", source.MimeType)
	c.Header("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, sanitizeFilename(source.Filename)))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, no-store")
	if source.Size > 0 {
		c.Header("Content-Length", strconv.FormatInt(source.Size, 10))
	}
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, source.Body); err != nil {
		// Headers are already sent, so a mid-stream failure can only truncate
		// the response and be logged; the client can retry the preview.
		slog.Warn("preview_stream_failed", "filename", source.Filename, "error", err)
	}
}

func sanitizeFilename(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, name)
	if strings.TrimSpace(cleaned) == "" {
		return "document"
	}
	return cleaned
}
