package httpapi

import (
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/example/rag-platform/internal/config"
	"github.com/example/rag-platform/internal/platform/observability"
	"github.com/gin-gonic/gin"
)

func NewRouter(cfg config.Config) *gin.Engine {
	return NewRouterWithLogger(cfg, slog.Default())
}

func NewRouterWithLogger(cfg config.Config, logger *slog.Logger) *gin.Engine {
	if cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(corsMiddleware(cfg), requestLogger(logger), metricsMiddleware(), gin.CustomRecovery(func(c *gin.Context, recovered any) {
		logger.Error("http_panic", "path", c.Request.URL.Path, "panic", recovered)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal_server_error"})
	}))

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/readyz", func(c *gin.Context) {
		checks := map[string]string{
			"postgres":  probeTCP(cfg.DatabaseURL),
			"redis":     probeTCP(cfg.RedisAddr),
			"docreader": probeTCP(cfg.DocreaderAddr),
			"minio":     probeTCP(cfg.S3Endpoint),
		}
		ready := true
		for _, result := range checks {
			if result != "ok" {
				ready = false
				break
			}
		}

		status := "ok"
		code := http.StatusOK
		if !ready {
			status = "not_ready"
			code = http.StatusServiceUnavailable
		}
		c.JSON(code, gin.H{
			"status":            status,
			"app_env":           cfg.AppEnv,
			"checks":            checks,
			"parser_sandbox":    cfg.ParserSandbox,
			"supported_formats": cfg.SupportedFormats,
		})
	})

	v1 := r.Group("/api/v1")
	v1.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "version": "v1"})
	})

	return r
}

func corsMiddleware(cfg config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if cfg.AppEnv == "development" || cfg.AppEnv == "test" {
			if origin == "http://localhost:5173" || origin == "http://127.0.0.1:5173" {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Access-Control-Allow-Credentials", "true")
				c.Header("Vary", "Origin")
			}
		}
		c.Header("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, X-Dev-User-ID, X-Dev-Tenant-ID, X-Request-ID, X-Trace-ID, Authorization")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if c.Request.Method == http.MethodOptions {
			c.Status(http.StatusNoContent)
			c.Abort()
			return
		}
		c.Next()
	}
}

// metricsMiddleware records request count and latency. c.FullPath() is the
// route template rather than the concrete URL, so metric cardinality stays
// bounded no matter how many document ids are requested.
func metricsMiddleware() gin.HandlerFunc {
	registry := observability.Default()
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		path := c.FullPath()
		if path == "" {
			path = "unmatched"
		}
		registry.Inc("http_requests_total", observability.Labels("method", c.Request.Method, "path", path, "status", strconv.Itoa(c.Writer.Status())))
		registry.Observe("http_request_duration_ms", time.Since(started), observability.Labels("method", c.Request.Method, "path", path))
	}
}

func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		logger.Info("http_request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(started).Milliseconds(),
		)
	}
}

func probeTCP(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "not_configured"
	}

	address := raw
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		address = parsed.Host
	}
	if !strings.Contains(address, ":") {
		return "invalid_address"
	}

	conn, err := net.DialTimeout("tcp", address, 800*time.Millisecond)
	if err != nil {
		return "unreachable"
	}
	_ = conn.Close()
	return "ok"
}
