package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/example/rag-platform/internal/ingestion"
	"github.com/gin-gonic/gin"
)

// writeSourcePreview streams the original file without ever exposing the object
// storage location. Active content types are never served inline.
func writeSourcePreview(c *gin.Context, source ingestion.SourceDocument) {
	disposition := "attachment"
	switch source.MimeType {
	case "application/pdf", "text/markdown", "text/plain":
		disposition = "inline"
	}
	c.Header("Content-Type", source.MimeType)
	c.Header("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, sanitizeFilename(source.Filename)))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, no-store")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(source.Content)
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
