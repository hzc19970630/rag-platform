package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/platform/tenantquota"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

// RegisterTenantRoutes exposes the tenant administration surface. Every route
// is session-only and owner/admin gated; the tenant is always taken from the
// authenticated principal, never from the request body.
func RegisterTenantRoutes(protected *gin.RouterGroup, deps TenantDeps, mw MiddlewareDeps) {
	tenantRoutes := protected.Group("/tenant")
	tenantRoutes.Use(requireOperator(mw))
	tenantRoutes.GET("", func(c *gin.Context) {
		overview, err := deps.Tenant.Overview(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.TenantOverview(overview))
	})
	tenantRoutes.GET("/quota", func(c *gin.Context) {
		effective, err := deps.Tenant.Quota(c.Request.Context(), accessContext(c).Principal)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.TenantQuota(effective))
	})
	tenantRoutes.PATCH("/quota", func(c *gin.Context) {
		var input httpapi.TenantQuotaUpdateRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		effective, err := deps.Tenant.UpdateQuota(c.Request.Context(), accessContext(c).Principal, tenantquota.Overrides{
			MaxDocuments:       input.MaxDocuments,
			MaxStorageBytes:    input.MaxStorageBytes,
			ChatRequestsPerDay: input.ChatRequestsPerDay,
		})
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.TenantQuota(effective))
	})
}
