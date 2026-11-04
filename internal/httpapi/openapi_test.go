package httpapi

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/example/rag-platform/internal/config"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

type openAPIDocument struct {
	Paths map[string]map[string]any `yaml:"paths"`
}

func TestOpenAPICoversEveryBusinessRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router, config.Config{AppEnv: "test"}, nil, slog.Default())

	spec := loadOpenAPI(t)
	registered := map[string]bool{}
	business := 0
	for _, route := range router.Routes() {
		if !strings.HasPrefix(route.Path, "/api/v1/") {
			continue
		}
		business++
		path := openAPIPath(route.Path)
		registered[path+" "+strings.ToLower(route.Method)] = true
		operation, ok := spec.Paths[path][strings.ToLower(route.Method)]
		if !ok || operation == nil {
			t.Fatalf("route %s %s is missing from openapi.yaml", route.Method, route.Path)
		}
	}
	// The milestone baseline. Changing it means consciously adding or removing a
	// public route, so the contract and the router stay in step.
	if business != 87 {
		t.Fatalf("business routes = %d, want 87", business)
	}
	for path, methods := range spec.Paths {
		for method := range methods {
			if !registered[path+" "+method] {
				t.Fatalf("openapi.yaml documents %s %s but no route registers it", strings.ToUpper(method), path)
			}
		}
	}
}

func loadOpenAPI(t *testing.T) openAPIDocument {
	t.Helper()
	raw, err := os.ReadFile("../../api/openapi/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var spec openAPIDocument
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	if len(spec.Paths) == 0 {
		t.Fatalf("openapi.yaml has no paths")
	}
	return spec
}

func openAPIPath(ginPath string) string {
	parts := strings.Split(ginPath, "/")
	for index, part := range parts {
		if strings.HasPrefix(part, ":") {
			parts[index] = "{" + part[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}
