package observability

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLabelsProducesStableMetricKeys(t *testing.T) {
	registry := New()
	registry.Inc("http_requests_total", Labels("status", "200", "method", "GET"))
	registry.Inc("http_requests_total", Labels("method", "GET", "status", "200"))
	registry.Inc("http_requests_total", Labels("method", "POST", "status", "500"))

	snapshot := registry.Snapshot()
	counters := snapshot["counters"].(map[string]int64)
	if counters["http_requests_total{method=GET,status=200}"] != 2 {
		t.Fatalf("label order must not split a metric: %#v", counters)
	}
	if counters["http_requests_total{method=POST,status=500}"] != 1 {
		t.Fatalf("distinct labels must stay distinct: %#v", counters)
	}
}

func TestObserveAggregatesCountSumMaxAndAverage(t *testing.T) {
	registry := New()
	registry.Observe("retrieval_duration_ms", 10*time.Millisecond, nil)
	registry.Observe("retrieval_duration_ms", 30*time.Millisecond, nil)

	raw, err := json.Marshal(registry.Snapshot()["timings"])
	if err != nil {
		t.Fatalf("snapshot must be JSON encodable: %v", err)
	}
	var timings map[string]Timing
	if err := json.Unmarshal(raw, &timings); err != nil {
		t.Fatalf("decode timings: %v", err)
	}
	timing := timings["retrieval_duration_ms"]
	if timing.Count != 2 || timing.SumMS != 40 || timing.MaxMS != 30 {
		t.Fatalf("timing = %+v, want count=2 sum=40 max=30", timing)
	}
	// Cumulative fixed buckets: 1,5,10,25,50,100,250,500,1000,2500,5000,10000.
	for index, want := range map[int]int64{2: 1, 3: 1, 4: 2, 11: 2} {
		if timing.Buckets[index] != want {
			t.Fatalf("bucket %d = %d, want %d (buckets: %v)", index, timing.Buckets[index], want, timing.Buckets)
		}
	}
	if _, ok := registry.Snapshot()["startedAt"]; !ok {
		t.Fatal("snapshot must expose the collection start time")
	}
}

func TestWritePrometheusRendersCountersAndTimings(t *testing.T) {
	registry := New()
	registry.Inc("ingestion_stage_total", Labels("stage", "embed", "result", "success"))
	registry.Observe("retrieval_duration_ms", 10*time.Millisecond, nil)
	registry.Observe("retrieval_duration_ms", 30*time.Millisecond, nil)

	var buffer bytes.Buffer
	if err := registry.WritePrometheus(&buffer); err != nil {
		t.Fatalf("write prometheus: %v", err)
	}
	output := buffer.String()
	for _, fragment := range []string{
		"# TYPE ingestion_stage_total counter",
		`ingestion_stage_total{result="success",stage="embed"} 1`,
		"# TYPE retrieval_duration_ms histogram",
		`retrieval_duration_ms_bucket{le="25"} 1`,
		`retrieval_duration_ms_bucket{le="50"} 2`,
		`retrieval_duration_ms_bucket{le="+Inf"} 2`,
		"retrieval_duration_ms_count 2",
		"retrieval_duration_ms_sum 40",
		"# TYPE retrieval_duration_ms_max gauge",
		"retrieval_duration_ms_max 30",
	} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("prometheus output is missing %q:\n%s", fragment, output)
		}
	}
	if strings.Contains(output, "# TYPE retrieval_duration_ms summary") {
		t.Fatalf("timings must no longer be exported as summaries:\n%s", output)
	}
}

func TestDefaultRegistryIsProcessWide(t *testing.T) {
	Default().Inc("m4_test_counter", nil)
	Default().Inc("m4_test_counter", nil)
	if Default().Snapshot()["counters"].(map[string]int64)["m4_test_counter"] != 2 {
		t.Fatal("Default must return the shared registry")
	}
}
