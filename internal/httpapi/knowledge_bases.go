package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/knowledgebase"
	"github.com/example/rag-platform/internal/platform/persistence/models"
	"github.com/gin-gonic/gin"
)

func RegisterKnowledgeBaseRoutes(router *gin.Engine, cfg config.Config, deps KnowledgeBaseDeps, mw MiddlewareDeps, logger *slog.Logger) {
	protected := router.Group("/api/v1")
	protected.Use(authenticationMiddleware(cfg, mw))

	knowledgeBases := protected.Group("/knowledge-bases")
	knowledgeBases.GET("", func(c *gin.Context) {
		rows, err := deps.KnowledgeBase.List(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		items := make([]knowledgeBaseResponse, 0, len(rows))
		for _, row := range rows {
			items = append(items, knowledgeBaseView(row))
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	knowledgeBases.POST("", func(c *gin.Context) {
		var input knowledgebase.CreateInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.KnowledgeBase.Create(c.Request.Context(), accessContext(c).Principal, input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, knowledgeBaseView(row))
	})

	byID := knowledgeBases.Group("/:id")
	byID.GET("", func(c *gin.Context) {
		row, err := deps.KnowledgeBase.Get(c.Request.Context(), accessContext(c).Principal, c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, knowledgeBaseView(row))
	})
	byID.PATCH("", func(c *gin.Context) {
		var input knowledgebase.UpdateInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.KnowledgeBase.Update(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, knowledgeBaseView(row))
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
		items := make([]knowledgeBaseMemberResponse, 0, len(rows))
		for _, row := range rows {
			items = append(items, knowledgeBaseMemberView(row))
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	members.POST("", func(c *gin.Context) {
		var input knowledgebase.AddMemberInput
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		row, err := deps.KnowledgeBase.AddMember(c.Request.Context(), accessContext(c).Principal, c.Param("id"), input)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, knowledgeBaseMemberView(row))
	})
	members.DELETE("/:userID", func(c *gin.Context) {
		if err := deps.KnowledgeBase.RemoveMember(c.Request.Context(), accessContext(c).Principal, c.Param("id"), c.Param("userID")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
}

// knowledgeBaseResponse is the explicit wire contract; handlers do not expose
// GORM rows directly, so a column rename cannot silently change the API.
type knowledgeBaseResponse struct {
	ID                 string    `json:"id"`
	TenantID           string    `json:"tenantId"`
	OwnerID            string    `json:"ownerId"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	EmbeddingModel     string    `json:"embeddingModel"`
	EmbeddingDimension int       `json:"embeddingDimension"`
	ACLVersion         int64     `json:"aclVersion"`
	RetrievalConfig    string    `json:"retrievalConfig"`
	IndexingStrategy   string    `json:"indexingStrategy"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

func knowledgeBaseView(row models.KnowledgeBase) knowledgeBaseResponse {
	return knowledgeBaseResponse{
		ID: row.ID, TenantID: row.TenantID, OwnerID: row.OwnerID, Name: row.Name, Description: row.Description,
		EmbeddingModel: row.EmbeddingModel, EmbeddingDimension: row.EmbeddingDimension, ACLVersion: row.ACLVersion,
		RetrievalConfig: row.RetrievalConfig, IndexingStrategy: row.IndexingStrategy,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

type knowledgeBaseMemberResponse struct {
	KnowledgeBaseID string    `json:"knowledgeBaseId"`
	UserID          string    `json:"userId"`
	Role            string    `json:"role"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func knowledgeBaseMemberView(row models.KnowledgeBaseMember) knowledgeBaseMemberResponse {
	return knowledgeBaseMemberResponse{
		KnowledgeBaseID: row.KnowledgeBaseID, UserID: row.UserID, Role: row.Role,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
