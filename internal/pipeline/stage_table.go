package pipeline

// StageSpec is one row of a pipeline's stage layout. Processing/Completed are
// the pipeline-level statuses for the active and completed stage; EventType is
// the outbox event that hands work to the next stage (empty for the last row).
type StageSpec struct {
	Stage      string
	Processing string
	Completed  string
	EventType  string
}

// StageTable is the single source of a pipeline's stage order and transitions.
// A new pipeline declares its rows once; callers no longer hand-write switches
// for next-stage lookup, status names and event names.
type StageTable struct {
	specs []StageSpec
	index map[string]int
}

func NewStageTable(specs ...StageSpec) *StageTable {
	table := &StageTable{specs: append([]StageSpec(nil), specs...), index: make(map[string]int, len(specs))}
	for i, spec := range table.specs {
		table.index[spec.Stage] = i
	}
	return table
}

func (t *StageTable) Spec(stage string) (StageSpec, bool) {
	index, ok := t.index[stage]
	if !ok {
		return StageSpec{}, false
	}
	return t.specs[index], true
}

func (t *StageTable) Next(stage string) (StageSpec, bool) {
	index, ok := t.index[stage]
	if !ok || index+1 >= len(t.specs) {
		return StageSpec{}, false
	}
	return t.specs[index+1], true
}

func (t *StageTable) Stages() []string {
	stages := make([]string, len(t.specs))
	for i, spec := range t.specs {
		stages[i] = spec.Stage
	}
	return stages
}
