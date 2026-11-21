package httpapi

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSplitCSVTrimsAndDropsEmptyValues(t *testing.T) {
	if got := splitCSV("  "); got != nil {
		t.Fatalf("blank = %v", got)
	}
	got := splitCSV(" a, b ,,c ")
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("split = %v", got)
	}
}

func TestReadinessStatusDefaultsToUnreachable(t *testing.T) {
	if got := readinessStatus("  "); got != "unreachable" {
		t.Fatalf("blank = %q", got)
	}
	if got := readinessStatus("ok"); got != "ok" {
		t.Fatalf("status = %q", got)
	}
}

func TestIntQueryParsesOnlyNonNegativeIntegers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/?limit=25&bad=x&negative=-1", nil)
	if got := intQuery(context, "limit"); got != 25 {
		t.Fatalf("limit = %d", got)
	}
	if got := intQuery(context, "missing"); got != 0 {
		t.Fatalf("missing = %d", got)
	}
	if got := intQuery(context, "bad"); got != 0 {
		t.Fatalf("bad = %d", got)
	}
	if got := intQuery(context, "negative"); got != 0 {
		t.Fatalf("negative = %d", got)
	}
}

func TestClientIPFallsBackToRemoteAddress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	context.Request.RemoteAddr = "192.0.2.10:1234"
	if got := clientIP(context); got != "192.0.2.10" {
		t.Fatalf("client ip = %q", got)
	}
}

func TestProbeTCPReportsUnconfigured(t *testing.T) {
	if got := probeTCP("  "); got != "not_configured" {
		t.Fatalf("probe = %q", got)
	}
}

func TestRequestIDAndBearerHelpers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	if got := requestIDFor(context); got != "" {
		t.Fatalf("empty request id = %q", got)
	}
	context.Request.Header.Set("X-Request-ID", " rid-1 ")
	if got := requestIDFor(context); got != "rid-1" {
		t.Fatalf("request id = %q", got)
	}
	if got := bearerToken(context); got != "" {
		t.Fatalf("empty bearer = %q", got)
	}
	context.Request.Header.Set("Authorization", "Basic abc")
	if got := bearerToken(context); got != "" {
		t.Fatalf("basic bearer = %q", got)
	}
	context.Request.Header.Set("Authorization", "Bearer token-1")
	if got := bearerToken(context); got != "token-1" {
		t.Fatalf("bearer = %q", got)
	}
}

func TestRequestIDOrDefaultPrefersProvided(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	if got := requestIDOrDefault(context, "provided"); got != "provided" {
		t.Fatalf("provided = %q", got)
	}
	context.Request.Header.Set("X-Request-ID", "header-id")
	if got := requestIDOrDefault(context, ""); got != "header-id" {
		t.Fatalf("header = %q", got)
	}
}
