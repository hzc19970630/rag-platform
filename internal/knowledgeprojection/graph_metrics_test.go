package knowledgeprojection

import (
	"testing"
	"time"

	"github.com/hzc19970630/rag-platform/internal/contracts/metrics"
	"github.com/hzc19970630/rag-platform/internal/relationstore"
)

type gaugeRecorder struct {
	values map[string]float64
}

func (g *gaugeRecorder) Inc(string, map[string]string)                        {}
func (g *gaugeRecorder) Observe(string, time.Duration, map[string]string)     {}
func (g *gaugeRecorder) SetGauge(name string, _ map[string]string, v float64) { g.values[name] = v }

func TestRecordGraphSnapshotSize(t *testing.T) {
	recorder := &gaugeRecorder{values: map[string]float64{}}
	service := &Service{metrics: recorder}

	service.recordGraphSnapshotSize(&relationstore.Graph{
		Entities:  []relationstore.Entity{{ID: "a"}, {ID: "b"}},
		Relations: []relationstore.Relation{{ID: "r1"}},
	})

	if got := recorder.values["graph_snapshot_entities"]; got != 2 {
		t.Fatalf("entities gauge = %v, want 2", got)
	}
	if got := recorder.values["graph_snapshot_relations"]; got != 1 {
		t.Fatalf("relations gauge = %v, want 1", got)
	}
}

func TestRecordGraphSnapshotSizeIgnoresRecorderWithoutGauges(t *testing.T) {
	(&Service{metrics: metrics.Nop{}}).recordGraphSnapshotSize(&relationstore.Graph{})
}
