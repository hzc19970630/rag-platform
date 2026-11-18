package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	contract "github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/ingestion"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

// RegisterKnowledgeSpaceRoutes owns the folder tree, manual tags and batch
// document actions. Authorization stays at the knowledge-base role until B6-b
// introduces the resource-level seam.
func RegisterKnowledgeSpaceRoutes(protected *gin.RouterGroup, cfg config.Config, deps IngestionDeps, mw MiddlewareDeps, logger *slog.Logger) {
	protected.GET("/knowledge-bases/:id/folders", func(c *gin.Context) {
		rows, err := deps.Ingestion.ListFolders(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.FolderList(rows))
	})

	protected.POST("/knowledge-bases/:id/folders", func(c *gin.Context) {
		var input contract.FolderCreateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.Ingestion.CreateFolder(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input.Name, input.ParentID)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, views.Folder(row))
	})

	protected.PATCH("/folders/:folderID", func(c *gin.Context) {
		var input contract.FolderUpdateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.Ingestion.UpdateFolder(c.Request.Context(), accessContext(c).Principal, c.Param("folderID"), ingestion.FolderUpdate{
			Name: input.Name, ParentID: input.ParentID,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.Folder(row))
	})

	protected.DELETE("/folders/:folderID", func(c *gin.Context) {
		if err := deps.Ingestion.DeleteFolder(c.Request.Context(), accessContext(c).Principal, c.Param("folderID"), c.Query("recursive") == "true"); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	protected.GET("/knowledge-bases/:id/tags", func(c *gin.Context) {
		rows, err := deps.Ingestion.ListTags(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.TagList(rows))
	})

	protected.POST("/knowledge-bases/:id/tags", func(c *gin.Context) {
		var input contract.TagCreateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.Ingestion.CreateTag(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input.Name)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, views.Tag(row))
	})

	protected.PATCH("/tags/:tagID", func(c *gin.Context) {
		var input contract.TagUpdateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.Ingestion.UpdateTag(c.Request.Context(), accessContext(c).Principal, c.Param("tagID"), input.Name)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.Tag(row))
	})

	protected.DELETE("/tags/:tagID", func(c *gin.Context) {
		if err := deps.Ingestion.DeleteTag(c.Request.Context(), accessContext(c).Principal, c.Param("tagID")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	protected.POST("/knowledge-bases/:id/documents/batch", func(c *gin.Context) {
		var input contract.BatchDocumentsRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		results, err := deps.Ingestion.BatchDocuments(c.Request.Context(), accessContext(c).Principal, c.Param("id"), ingestion.BatchInput{
			Action: input.Action, DocumentIDs: input.DocumentIDs, FolderID: strings.TrimSpace(input.FolderID), TagIDs: input.TagIDs,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.BatchResults(results))
	})
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
