package pipeline

import "testing"

func TestStageTableDrivesOrderStatusAndEvents(t *testing.T) {
	table := NewStageTable(
		StageSpec{Stage: "a", Processing: "A_RUN", Completed: "A_DONE", EventType: "event.b"},
		StageSpec{Stage: "b", Processing: "B_RUN", Completed: "B_DONE", EventType: "event.c"},
		StageSpec{Stage: "c", Processing: "C_RUN", Completed: "C_DONE"},
	)
	if stages := table.Stages(); len(stages) != 3 || stages[0] != "a" || stages[2] != "c" {
		t.Fatalf("Stages() = %v", stages)
	}
	spec, ok := table.Spec("b")
	if !ok || spec.Processing != "B_RUN" || spec.Completed != "B_DONE" || spec.EventType != "event.c" {
		t.Fatalf("Spec(b) = %+v, %v", spec, ok)
	}
	next, ok := table.Next("a")
	if !ok || next.Stage != "b" || next.EventType != "event.c" {
		t.Fatalf("Next(a) = %+v, %v", next, ok)
	}
	if _, ok := table.Next("c"); ok {
		t.Fatal("the last stage must not have a successor")
	}
	if _, ok := table.Next("missing"); ok {
		t.Fatal("an unknown stage must not have a successor")
	}
}
