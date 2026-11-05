// Package metrics is the injection port for the domain's process counters.
// Adapters implement it; domain packages never reach for a package-level
// registry, so tests and alternative backends can substitute a recorder.
package metrics

import "time"

// Recorder is the minimal metric surface used by domain services.
type Recorder interface {
	Inc(name string, labels map[string]string)
	Observe(name string, duration time.Duration, labels map[string]string)
}

// Nop discards every observation. It is the default so a service without an
// injected recorder keeps working in tests and offline tools.
type Nop struct{}

func (Nop) Inc(string, map[string]string)                    {}
func (Nop) Observe(string, time.Duration, map[string]string) {}

// Labels builds a label set from alternating key/value pairs.
func Labels(kv ...string) map[string]string {
	labels := make(map[string]string, len(kv)/2)
	for index := 0; index+1 < len(kv); index += 2 {
		labels[kv[index]] = kv[index+1]
	}
	return labels
}
