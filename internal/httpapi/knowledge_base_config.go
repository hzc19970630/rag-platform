package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/config"
	"github.com/hzc19970630/rag-platform/internal/configschema"
	"github.com/hzc19970630/rag-platform/internal/knowledgebase"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

func RegisterKnowledgeBaseConfigRoutes(protected *gin.RouterGroup, cfg config.Config, deps KnowledgeBaseDeps, mw MiddlewareDeps, logger *slog.Logger) {
	byID := protected.Group("/knowledge-bases/:id")
	byID.GET("/configuration", func(c *gin.Context) {
		view, err := deps.KnowledgeBase.ConfigurationWithTenant(
			c.Request.Context(),
			accessContext(c).Principal,
			c.Param("id"),
			cfg.Retrieval,
			cfg.RetrievalTier(),
			knowledgebase.ReadOnlySystemConfiguration(cfg),
		)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.KnowledgeBaseConfiguration(view))
	})

	byID.PUT("/retrieval-config", func(c *gin.Context) {
		// Validate the schema-keyed document before decoding it into the typed
		// override, so a typo or an unknown parameter is rejected instead of
		// silently dropped by encoding/json.
		var values map[string]json.RawMessage
		if err := c.ShouldBindJSON(&values); err != nil {
			writeError(c, err)
			return
		}
		if values == nil {
			values = map[string]json.RawMessage{}
		}
		if err := configschema.Validate(configschema.DomainRetrieval, values); err != nil {
			writeError(c, err)
			return
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			writeError(c, err)
			return
		}
		var input httpapi.RetrievalConfigOverride
		if err := json.Unmarshal(encoded, &input); err != nil {
			writeError(c, err)
			return
		}
		if _, err := deps.KnowledgeBase.ReplaceRetrievalConfig(c.Request.Context(), accessContext(c).Principal, c.Param("id"), views.ParamsOverride(input)); err != nil {
			writeError(c, err)
			return
		}
		view, err := deps.KnowledgeBase.ConfigurationWithTenant(
			c.Request.Context(),
			accessContext(c).Principal,
			c.Param("id"),
			cfg.Retrieval,
			cfg.RetrievalTier(),
			knowledgebase.ReadOnlySystemConfiguration(cfg),
		)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.KnowledgeBaseConfiguration(view))
	})

}
