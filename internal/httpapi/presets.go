package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/presets"
	"github.com/hzc19970630/rag-platform/internal/transport/views"
)

// PresetDeps exposes the deployment and tenant preset catalog.
type PresetDeps struct {
	Catalog *presets.Resolver
}

// RegisterPresetRoutes lists packs and lets tenant operators clone, import,
// edit or delete their own copies. Built-in packs stay immutable.
func RegisterPresetRoutes(protected *gin.RouterGroup, deps PresetDeps, mw MiddlewareDeps) {
	protected.GET("/presets", func(c *gin.Context) {
		if deps.Catalog == nil {
			c.JSON(http.StatusOK, views.PresetList(nil))
			return
		}
		entries, err := deps.Catalog.List(c.Request.Context(), accessContext(c).Principal.TenantID)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.PresetList(entries))
	})
	protected.GET("/presets/:name/:version", func(c *gin.Context) {
		manifest, err := deps.Catalog.Get(c.Request.Context(), accessContext(c).Principal.TenantID, c.Param("name"), c.Param("version"))
		if err != nil {
			writeError(c, err)
			return
		}
		source := deps.Catalog.Source(c.Request.Context(), accessContext(c).Principal.TenantID, c.Param("name"), c.Param("version"))
		c.JSON(http.StatusOK, views.PresetDetail(manifest, source))
	})

	writes := protected.Group("/presets")
	writes.Use(requireOperator(mw))
	writes.POST("", func(c *gin.Context) {
		manifest, err := bindPreset(c)
		if err != nil {
			writeError(c, err)
			return
		}
		principal := accessContext(c).Principal
		saved, err := deps.Catalog.Create(c.Request.Context(), principal.TenantID, principal.SubjectID, manifest)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, views.PresetDetail(saved, presets.SourceTenant))
	})
	writes.PUT("/:name/:version", func(c *gin.Context) {
		manifest, err := bindPreset(c)
		if err != nil {
			writeError(c, err)
			return
		}
		principal := accessContext(c).Principal
		saved, err := deps.Catalog.Update(c.Request.Context(), principal.TenantID, c.Param("name"), c.Param("version"), manifest)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, views.PresetDetail(saved, presets.SourceTenant))
	})
	writes.DELETE("/:name/:version", func(c *gin.Context) {
		if err := deps.Catalog.Delete(c.Request.Context(), accessContext(c).Principal.TenantID, c.Param("name"), c.Param("version")); err != nil {
			writeError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	writes.POST("/:name/:version/clone", func(c *gin.Context) {
		var input httpapi.PresetCloneRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			writeError(c, err)
			return
		}
		principal := accessContext(c).Principal
		saved, err := deps.Catalog.Clone(c.Request.Context(), principal.TenantID, principal.SubjectID,
			c.Param("name"), c.Param("version"), input.Name, input.Version)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusCreated, views.PresetDetail(saved, presets.SourceTenant))
	})
}

func bindPreset(c *gin.Context) (presets.Manifest, error) {
	var input httpapi.PresetWriteRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		return presets.Manifest{}, err
	}
	var manifest presets.Manifest
	if err := json.Unmarshal(input.Manifest, &manifest); err != nil {
		return presets.Manifest{}, err
	}
	return manifest, nil
}
