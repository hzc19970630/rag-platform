package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantdb"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

func RegisterDocumentRoutes(protected *gin.RouterGroup, cfg config.Config, deps IngestionDeps, mw MiddlewareDeps, logger *slog.Logger) {
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
			writeError(c, apperror.Invalidf("file exceeds maximum size"))
			return
		}
		idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		result, err := deps.Ingestion.Upload(c.Request.Context(), accessContext(c).Principal, ingestion.UploadInput{
			KnowledgeBaseID: c.Param("id"),
			DocumentID:      c.PostForm("document_id"),
			FolderID:        c.PostForm("folder_id"),
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
		c.JSON(http.StatusAccepted, views.UploadResult(result))
	})

	protected.GET("/knowledge-bases/:id/documents", func(c *gin.Context) {
		filter := ingestion.DocumentFilter{FolderID: strings.TrimSpace(c.Query("folder_id")), TagIDs: splitCSV(c.Query("tag_ids"))}
		page, err := deps.Ingestion.ListDocuments(c.Request.Context(), accessContext(c).Principal,
			c.Param("id"), filter, c.Query("cursor"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.DocumentPage(page))
	})

	protected.GET("/documents/:id", func(c *gin.Context) {
		result, err := deps.Ingestion.Get(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.DocumentView(result))
	})

	protected.DELETE("/documents/:id", func(c *gin.Context) {
		if err := deps.Ingestion.Delete(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	protected.GET("/knowledge-bases/:id/tables", func(c *gin.Context) {
		rows, err := deps.Ingestion.ListTableAssets(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.TableAssetList{Items: views.TableAssets(rows)})
	})
	protected.POST("/knowledge-bases/:id/tables/query", func(c *gin.Context) {
		var input httpapi.TableQueryRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		filters := make([]ingestion.TableFilter, 0, len(input.Filters))
		for _, filter := range input.Filters {
			filters = append(filters, ingestion.TableFilter{Column: filter.Column, Op: filter.Op, Value: filter.Value})
		}
		result, err := deps.Ingestion.QueryTable(c.Request.Context(), accessContext(c).Principal, c.Param("id"), ingestion.TableQuery{
			AssetID: input.AssetID, Filters: filters, OrderBy: input.OrderBy, Desc: input.Desc, Limit: input.Limit,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.TableQueryResult(result))
	})
	protected.POST("/knowledge-bases/:id/tables/sql", func(c *gin.Context) {
		var input httpapi.TableSQLRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		result, err := deps.Ingestion.QueryTableSQL(c.Request.Context(), accessContext(c).Principal, c.Param("id"), ingestion.TableSQLQuery{
			AssetID: input.AssetID, SQL: input.SQL, Params: input.Params, Preview: input.Preview, Limit: input.Limit,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.TableSQLResult(result))
	})

	documents := protected.Group("/documents/:id")
	documents.GET("/preview", func(c *gin.Context) {
		source, err := deps.Ingestion.SourceDocument(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		// The body streams for as long as the client reads it; release the
		// request transaction before the first response byte.
		if err := tenantdb.CommitRequestTx(c.Request.Context()); err != nil {
			if source.Body != nil {
				_ = source.Body.Close()
			}
			writeError(c, err)
			return
		}
		writeSourcePreview(c, source)
	})
	documents.GET("/content", func(c *gin.Context) {
		preview, err := deps.Ingestion.Preview(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.DocumentPreview(preview))
	})
	documents.GET("/chunks", func(c *gin.Context) {
		page, err := deps.Ingestion.Chunks(c.Request.Context(), accessContext(c).Principal, c.Param("id"), intQuery(c, "cursor"), intQuery(c, "limit"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.ChunkPage(page))
	})
	documents.GET("/status", func(c *gin.Context) {
		status, err := deps.Ingestion.Status(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.DocumentStatus(status))
	})
	documents.GET("/index-estimate", func(c *gin.Context) {
		estimate, err := deps.Ingestion.IndexEstimate(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.DocumentIndexEstimate(estimate))
	})
	documents.POST("/index-mode", func(c *gin.Context) {
		var input httpapi.DocumentIndexModeRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		if _, err := deps.Ingestion.SetIndexMode(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input.Mode); err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, httpapi.StartResult{Started: 1})
	})

	protected.GET("/chunks/:chunkID", func(c *gin.Context) {
		chunk, err := deps.Ingestion.ChunkDetail(c.Request.Context(), accessContext(c).Principal, c.Param("chunkID"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.Chunk(chunk))
	})

	documents.GET("/versions", func(c *gin.Context) {
		page, err := deps.Ingestion.ListVersions(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.DocumentVersionPage(page))
	})
	documents.POST("/reparse", func(c *gin.Context) {
		result, err := deps.Ingestion.Reparse(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, views.UploadResult(result))
	})
	documents.POST("/tables/rebuild", func(c *gin.Context) {
		result, err := deps.Ingestion.RebuildTables(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, views.UploadResult(result))
	})
	documents.POST("/versions/:versionID/activate", func(c *gin.Context) {
		view, err := deps.Ingestion.ActivateVersion(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("versionID"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.DocumentView(view))
	})
	documents.POST("/restore", func(c *gin.Context) {
		document, err := deps.Ingestion.Restore(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.DocumentEnvelope(document))
	})
	documents.POST("/purge", func(c *gin.Context) {
		if err := deps.Ingestion.Purge(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
}
