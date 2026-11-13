package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

// RegisterModelRoutes exposes the tenant model catalog: a read-only merged view
// for knowledge-base settings and the operator-only provider CRUD. Provider
// writes are session-only and gated by tenant:admin; the catalog read is not,
// because an editor binding a model must see the options.
func RegisterModelRoutes(protected *gin.RouterGroup, deps ModelDeps, mw MiddlewareDeps) {
	models := protected.Group("/models")
	models.GET("/catalog", func(c *gin.Context) {
		items, err := deps.Models.ListCatalog(c.Request.Context(), accessContext(c).Principal.TenantID)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.ModelCatalog(items))
	})

	providers := models.Group("/providers")
	providers.Use(requireOperator(mw))
	providers.GET("", func(c *gin.Context) {
		items, err := deps.Models.ListProviders(c.Request.Context(), accessContext(c).Principal.TenantID)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, httpapi.ModelProviderList{Items: views.ModelProviders(items)})
	})
	providers.POST("", func(c *gin.Context) {
		var input httpapi.ModelProviderUpsertRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		provider, err := deps.Models.CreateProvider(c.Request.Context(), accessContext(c).Principal.TenantID, modelActor(c), views.ModelProviderInput(input))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, views.ModelProvider(provider))
	})
	providers.PATCH("/:id", func(c *gin.Context) {
		var input httpapi.ModelProviderUpsertRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		provider, err := deps.Models.UpdateProvider(c.Request.Context(), accessContext(c).Principal.TenantID, modelActor(c), c.Param("id"), views.ModelProviderInput(input))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.ModelProvider(provider))
	})
	providers.DELETE("/:id", func(c *gin.Context) {
		if err := deps.Models.DeleteProvider(c.Request.Context(), accessContext(c).Principal.TenantID, modelActor(c), c.Param("id")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	providers.POST("/:id/test", func(c *gin.Context) {
		result, err := deps.Models.TestProvider(c.Request.Context(), accessContext(c).Principal.TenantID, modelActor(c), c.Param("id"))
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.ModelProviderTestResult(result))
	})
}

func modelActor(c *gin.Context) modelregistry.Actor {
	principal := accessContext(c).Principal
	return modelregistry.Actor{
		ID: principal.SubjectID, RequestID: principal.RequestID,
		IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
	}
}
