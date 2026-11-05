// Package observability is the small, dependency-free metrics surface for the
// M4 acceptance gates: queue depth, parse failure rate, retrieval latency and
// model errors. Counters are process-local and reset on restart, which is
// sufficient because the durable facts (stage runs, messages) remain queryable.
package observability

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Registry struct {
	mu       sync.Mutex
	counters map[string]int64
	timings  map[string]*Timing
	started  time.Time
}

type Timing struct {
	Count   int64   `json:"count"`
	SumMS   float64 `json:"sumMs"`
	MaxMS   float64 `json:"maxMs"`
	Buckets []int64 `json:"buckets"`
}

// timingBucketBoundsMS is the fixed histogram ladder. Buckets are cumulative:
// every observation increments each bound it falls into, so le="+Inf" equals
// Count.
var timingBucketBoundsMS = []int64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

var defaultRegistry = New()

func New() *Registry {
	return &Registry{counters: map[string]int64{}, timings: map[string]*Timing{}, started: time.Now()}
}

func Default() *Registry { return defaultRegistry }

// Labels builds a label set from alternating key/value pairs. It keeps call
// sites readable and makes the metric key deterministic.
func Labels(kv ...string) map[string]string {
	labels := make(map[string]string, len(kv)/2)
	for index := 0; index+1 < len(kv); index += 2 {
		labels[kv[index]] = kv[index+1]
	}
	return labels
}

func (r *Registry) Inc(name string, labels map[string]string) {
	key := metricKey(name, labels)
	r.mu.Lock()
	r.counters[key]++
	r.mu.Unlock()
}

// Add increments a counter by a batch size. It mirrors metrics.Adder so the
// projection cache can report package counts in one lock acquisition.
func (r *Registry) Add(name string, value int64, labels map[string]string) {
	if value == 0 {
		return
	}
	key := metricKey(name, labels)
	r.mu.Lock()
	r.counters[key] += value
	r.mu.Unlock()
}

func (r *Registry) Observe(name string, duration time.Duration, labels map[string]string) {
	key := metricKey(name, labels)
	milliseconds := float64(duration.Microseconds()) / 1000.0
	r.mu.Lock()
	timing, ok := r.timings[key]
	if !ok {
		timing = &Timing{Buckets: make([]int64, len(timingBucketBoundsMS))}
		r.timings[key] = timing
	}
	timing.Count++
	timing.SumMS += milliseconds
	if milliseconds > timing.MaxMS {
		timing.MaxMS = milliseconds
	}
	for index, bound := range timingBucketBoundsMS {
		if milliseconds <= float64(bound) {
			timing.Buckets[index]++
		}
	}
	r.mu.Unlock()
}

func (r *Registry) Snapshot() map[string]any {
	r.mu.Lock()
	counters := make(map[string]int64, len(r.counters))
	for key, value := range r.counters {
		counters[key] = value
	}
	timings := make(map[string]any, len(r.timings))
	for key, value := range r.timings {
		entry := map[string]any{
			"count": value.Count, "sumMs": round(value.SumMS), "maxMs": round(value.MaxMS),
			"buckets": append([]int64(nil), value.Buckets...),
		}
		if value.Count > 0 {
			entry["avgMs"] = round(value.SumMS / float64(value.Count))
		}
		timings[key] = entry
	}
	started := r.started
	r.mu.Unlock()
	return map[string]any{
		"startedAt": started.UTC().Format(time.RFC3339),
		"counters":  counters,
		"timings":   timings,
	}
}

func metricKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, labels[key]))
	}
	return name + "{" + strings.Join(parts, ",") + "}"
}

func round(value float64) float64 {
	return float64(int64(value*1000+0.5)) / 1000
}

// WritePrometheus renders the process-local registry in the Prometheus text
// exposition format. Timings are histograms with a fixed millisecond ladder, so
// P95 can be computed with histogram_quantile; the legacy max value stays
// exported as a separate gauge for existing panels.
func (r *Registry) WritePrometheus(writer io.Writer) error {
	r.mu.Lock()
	counters := make(map[string]int64, len(r.counters))
	for key, value := range r.counters {
		counters[key] = value
	}
	timings := make(map[string]Timing, len(r.timings))
	for key, value := range r.timings {
		copied := *value
		copied.Buckets = append([]int64(nil), value.Buckets...)
		timings[key] = copied
	}
	r.mu.Unlock()

	var lines []string
	declared := map[string]bool{}
	keys := make([]string, 0, len(counters))
	for key := range counters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		name, labels := splitMetricKey(key)
		if !declared[name] {
			lines = append(lines, "# TYPE "+name+" counter")
			declared[name] = true
		}
		lines = append(lines, fmt.Sprintf("%s%s %d", name, renderLabels(labels), counters[key]))
	}

	keys = keys[:0]
	for key := range timings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	declaredMax := map[string]bool{}
	for _, key := range keys {
		name, labels := splitMetricKey(key)
		timing := timings[key]
		if !declared[name] {
			lines = append(lines, "# TYPE "+name+" histogram")
			declared[name] = true
		}
		for index, bound := range timingBucketBoundsMS {
			var count int64
			if index < len(timing.Buckets) {
				count = timing.Buckets[index]
			}
			lines = append(lines, fmt.Sprintf("%s_bucket%s %d", name,
				renderLabels(mergeLabels(labels, "le", strconv.FormatInt(bound, 10))), count))
		}
		lines = append(lines,
			fmt.Sprintf("%s_bucket%s %d", name, renderLabels(mergeLabels(labels, "le", "+Inf")), timing.Count),
			fmt.Sprintf("%s_count%s %d", name, renderLabels(labels), timing.Count),
			fmt.Sprintf("%s_sum%s %v", name, renderLabels(labels), round(timing.SumMS)),
		)
		if !declaredMax[name] {
			lines = append(lines, "# TYPE "+name+"_max gauge")
			declaredMax[name] = true
		}
		lines = append(lines, fmt.Sprintf("%s_max%s %v", name, renderLabels(labels), round(timing.MaxMS)))
	}
	_, err := io.WriteString(writer, strings.Join(lines, "\n")+"\n")
	return err
}

func splitMetricKey(key string) (string, map[string]string) {
	open := strings.IndexByte(key, '{')
	if open < 0 || !strings.HasSuffix(key, "}") {
		return key, nil
	}
	name := key[:open]
	labels := map[string]string{}
	for _, pair := range strings.Split(key[open+1:len(key)-1], ",") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			labels[parts[0]] = parts[1]
		}
	}
	return name, labels
}

func mergeLabels(labels map[string]string, key, value string) map[string]string {
	merged := make(map[string]string, len(labels)+1)
	for name, existing := range labels {
		merged[name] = existing
	}
	merged[key] = value
	return merged
}

func renderLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, key, escapeLabel(labels[key])))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return strings.ReplaceAll(value, "\n", `\n`)
}
