package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	contract "github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

// RegisterUploadRoutes owns resumable upload sessions. Batch uploads are
// orchestrated by the client as one session per file; there is deliberately no
// server-side batch transaction.
func RegisterUploadRoutes(protected *gin.RouterGroup, cfg config.Config, deps IngestionDeps, mw MiddlewareDeps, logger *slog.Logger) {
	protected.POST("/knowledge-bases/:id/uploads", func(c *gin.Context) {
		var input contract.UploadSessionCreateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		result, err := deps.Ingestion.CreateUploadSession(c.Request.Context(), accessContext(c).Principal, ingestion.CreateUploadInput{
			KnowledgeBaseID: c.Param("id"),
			DocumentID:      input.DocumentID,
			FolderID:        input.FolderID,
			Filename:        input.Filename,
			MimeType:        input.MimeType,
			SizeBytes:       input.SizeBytes,
			IdempotencyKey:  c.GetHeader("Idempotency-Key"),
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, views.UploadSession(result))
	})

	protected.GET("/uploads/:id", func(c *gin.Context) {
		result, err := deps.Ingestion.GetUploadSession(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.UploadSession(result))
	})

	protected.POST("/uploads/:id/urls", func(c *gin.Context) {
		result, err := deps.Ingestion.RefreshUploadPartURLs(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.UploadSession(result))
	})

	// Proxy transport streams one part through the API. The body is bounded to
	// the declared Content-Length, and the service re-checks the size because a
	// caller controls that header.
	protected.PUT("/uploads/:id/parts/:part", func(c *gin.Context) {
		part, err := strconv.Atoi(c.Param("part"))
		if err != nil || part < 1 {
			writeError(c, apperror.Invalidf("upload part is out of range"))
			return
		}
		size := c.Request.ContentLength
		if size <= 0 {
			writeError(c, apperror.Invalidf("upload part requires a content length"))
			return
		}
		if cfg.MaxFileBytes > 0 && size > cfg.MaxFileBytes {
			writeError(c, apperror.Invalidf("file exceeds maximum size"))
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, size)
		session, err := deps.Ingestion.PutUploadPart(c.Request.Context(), accessContext(c).Principal, c.Param("id"), part, c.Request.Body, size)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.UploadSession(ingestion.UploadSessionResult{Session: session}))
	})

	protected.POST("/uploads/:id/complete", func(c *gin.Context) {
		result, err := deps.Ingestion.CompleteUploadSession(c.Request.Context(), accessContext(c).Principal, c.Param("id"),
			c.GetHeader("X-Request-ID"), c.GetHeader("X-Trace-ID"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, views.UploadResult(result))
	})

	protected.DELETE("/uploads/:id", func(c *gin.Context) {
		if err := deps.Ingestion.AbortUploadSession(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
}
