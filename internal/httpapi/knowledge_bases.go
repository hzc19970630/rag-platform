package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/example/rag-platform/internal/app"
	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/knowledgebase"
	"github.com/gin-gonic/gin"
)

func RegisterKnowledgeBaseRoutes(router *gin.Engine, cfg config.Config, application *app.Application, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, application))

	knowledgeBases := protected.Group("/knowledge-bases")
	knowledgeBases.GET("", func(c *gin.Context) {
		rows, err := application.KnowledgeBase.List(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows})
	})
	knowledgeBases.POST("", func(c *gin.Context) {
		var input knowledgebase.CreateInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := application.KnowledgeBase.Create(c.Request.Context(), accessContext(c).Principal, input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, row)
	})

	byID := knowledgeBases.Group("/:id")
	byID.GET("", func(c *gin.Context) {
		row, err := application.KnowledgeBase.Get(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, row)
	})
	byID.PATCH("", func(c *gin.Context) {
		var input knowledgebase.UpdateInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := application.KnowledgeBase.Update(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, row)
	})
	byID.DELETE("", func(c *gin.Context) {
		if err := application.KnowledgeBase.Delete(c.Request.Context(), accessContext(c).Principal, c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})

	members := byID.Group("/members")
	members.GET("", func(c *gin.Context) {
		rows, err := application.KnowledgeBase.ListMembers(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": rows})
	})
	members.POST("", func(c *gin.Context) {
		var input knowledgebase.AddMemberInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := application.KnowledgeBase.AddMember(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, row)
	})
	members.DELETE("/:userID", func(c *gin.Context) {
		if err := application.KnowledgeBase.RemoveMember(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("userID")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
}
