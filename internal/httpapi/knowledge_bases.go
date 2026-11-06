package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/example/rag-platform/contracts/httpapi"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/transport/views"
	"github.com/gin-gonic/gin"
)

func RegisterKnowledgeBaseRoutes(protected *gin.RouterGroup, cfg config.Config, deps KnowledgeBaseDeps, mw MiddlewareDeps, logger *slog.Logger) {
	knowledgeBases := protected.Group("/knowledge-bases")
	knowledgeBases.GET("", func(c *gin.Context) {
		rows, err := deps.KnowledgeBase.List(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		items := make([]httpapi.KnowledgeBase, 0, len(rows))
		for _, row := range rows {
			items = append(items, views.KnowledgeBase(row))
		}
		c.JSON(http.StatusOK, httpapi.KnowledgeBaseList{Items: items})
	})
	knowledgeBases.POST("", func(c *gin.Context) {
		var input httpapi.KnowledgeBaseCreateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.KnowledgeBase.Create(c.Request.Context(), accessContext(c).Principal, views.CreateKnowledgeBaseInput(input))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, views.KnowledgeBase(row))
	})

	byID := knowledgeBases.Group("/:id")
	byID.GET("", func(c *gin.Context) {
		row, err := deps.KnowledgeBase.Get(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.KnowledgeBase(row))
	})
	byID.PATCH("", func(c *gin.Context) {
		var input httpapi.KnowledgeBaseUpdateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.KnowledgeBase.Update(c.Request.Context(), accessContext(c).Principal, c.Param("id"), views.UpdateKnowledgeBaseInput(input))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.KnowledgeBase(row))
	})
	byID.DELETE("", func(c *gin.Context) {
		if err := deps.KnowledgeBase.Delete(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	members := byID.Group("/members")
	members.GET("", func(c *gin.Context) {
		rows, err := deps.KnowledgeBase.ListMembers(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		items := make([]httpapi.KnowledgeBaseMember, 0, len(rows))
		for _, row := range rows {
			items = append(items, views.KnowledgeBaseMember(row))
		}
		c.JSON(http.StatusOK, httpapi.KnowledgeBaseMemberList{Items: items})
	})
	members.POST("", func(c *gin.Context) {
		var input httpapi.KnowledgeBaseMemberCreateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.KnowledgeBase.AddMember(c.Request.Context(), accessContext(c).Principal, c.Param("id"), views.AddKnowledgeBaseMemberInput(input))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, views.KnowledgeBaseMember(row))
	})
	members.DELETE("/:userID", func(c *gin.Context) {
		if err := deps.KnowledgeBase.RemoveMember(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("userID")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
}
