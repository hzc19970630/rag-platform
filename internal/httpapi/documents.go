package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/ingestion"
	"github.com/gin-gonic/gin"
)

func RegisterDocumentRoutes(router *gin.Engine, cfg config.Config, application *app.Application, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, application))

	protected.POST(documentUploadPath, func(c *gin.Context) {
		// The global JSON cap skips multipart, so the upload route bounds the
		// envelope itself before FormFile parses it into memory or temp files.
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cfg.MaxFileBytes+maxMultipartEnvelopeBytes)
		file, header, err := c.Request.FormFile("file")
		if err != nil {
			writeError(c, err)
			return
		}
		defer file.Close()
		content, err := io.ReadAll(io.LimitReader(file, cfg.MaxFileBytes+1))
		if err != nil {
			writeError(c, err)
			return
		}
		if int64(len(content)) > cfg.MaxFileBytes {
			writeError(c, ginError("file exceeds maximum size"))
			return
		}
		idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		result, err := application.Ingestion.Upload(c.Request.Context(), accessContext(c).Principal, ingestion.UploadInput{
			KnowledgeBaseID: c.Param("id"),
			DocumentID:      c.PostForm("document_id"),
			Filename:        header.Filename,
			MimeType:        header.Header.Get("Content-Type"),
			Content:         content,
			IdempotencyKey:  idempotencyKey,
			RequestID:       c.GetHeader("X-Request-ID"),
			TraceID:         c.GetHeader("X-Trace-ID"),
			ActorID:         accessContext(c).Principal.SubjectID,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, result)
	})

	protected.GET("/knowledge-bases/:id/documents", func(c *gin.Context) {
		page, err := application.Ingestion.ListDocuments(c.Request.Context(), accessContext(c).Principal,
			c.Param("id"), c.Query("cursor"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
	})

	protected.GET("/documents/:id", func(c *gin.Context) {
		result, err := application.Ingestion.Get(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, result)
	})

	protected.DELETE("/documents/:id", func(c *gin.Context) {
		if err := application.Ingestion.Delete(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	documents := protected.Group("/documents/:id")
	documents.GET("/preview", func(c *gin.Context) {
		source, err := application.Ingestion.SourceDocument(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		writeSourcePreview(c, source)
	})
	documents.GET("/content", func(c *gin.Context) {
		preview, err := application.Ingestion.Preview(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, preview)
	})
	documents.GET("/chunks", func(c *gin.Context) {
		page, err := application.Ingestion.Chunks(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "cursor"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
	})
	documents.GET("/status", func(c *gin.Context) {
		status, err := application.Ingestion.Status(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, status)
	})

	protected.GET("/chunks/:chunkID", func(c *gin.Context) {
		chunk, err := application.Ingestion.ChunkDetail(c.Request.Context(), accessContext(c).Principal, c.Param("chunkID"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, chunk)
	})

	documents.GET("/versions", func(c *gin.Context) {
		page, err := application.Ingestion.ListVersions(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
	})
	documents.POST("/reparse", func(c *gin.Context) {
		result, err := application.Ingestion.Reparse(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, result)
	})
	documents.POST("/versions/:versionID/activate", func(c *gin.Context) {
		view, err := application.Ingestion.ActivateVersion(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("versionID"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	})
	documents.POST("/restore", func(c *gin.Context) {
		document, err := application.Ingestion.Restore(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"document": document})
	})
	documents.POST("/purge", func(c *gin.Context) {
		if err := application.Ingestion.Purge(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
}
