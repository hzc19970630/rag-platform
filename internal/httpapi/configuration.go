package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/modelregistry"
	"github.com/hzc19970630/rag-platform/internal/tenantconfig"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

// RegisterConfigurationRoutes exposes the tenant business configuration. The
// tenant is always the authenticated principal's tenant; there is no tenant id
// in the path or body, so one tenant cannot read or write another's policy.
func RegisterConfigurationRoutes(protected *gin.RouterGroup, deps ConfigurationDeps, mw MiddlewareDeps) {
	group := protected.Group("/tenant/config")
	group.Use(requireOperator(mw))

	group.GET("", func(c *gin.Context) {
		view, err := deps.TenantConfig.Get(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.TenantConfiguration(view))
	})

	group.PUT("", func(c *gin.Context) {
		var input httpapi.TenantConfigurationUpdateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		view, err := deps.TenantConfig.Put(c.Request.Context(), accessContext(c).Principal, tenantconfig.Input{
			Retrieval: input.Retrieval,
			Chunking:  input.Chunking,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.TenantConfiguration(view))
	})

	group.GET("/health", func(c *gin.Context) {
		principal := accessContext(c).Principal
		report, err := deps.Health.Health(c.Request.Context(), principal.TenantID)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.ConfigHealth(report))
	})

	group.POST("/test-connection", func(c *gin.Context) {
		var input httpapi.ConfigConnectionTestRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		principal := accessContext(c).Principal
		result, err := deps.Health.TestConnection(c.Request.Context(), principal.TenantID, modelregistry.Actor{
			ID: principal.SubjectID, RequestID: principal.RequestID,
			IPAddress: principal.IPAddress, UserAgent: principal.UserAgent,
		}, input.Capability, input.ProviderID)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.ConfigConnectionResult(result))
	})
}
