package vector

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func indexDefinition(name string, dimension int) string {
	return "CREATE INDEX " + name + " ON public.chunks USING hnsw (((embedding)::halfvec(" +
		strconv.Itoa(dimension) + ")) halfvec_cosine_ops) WITH (m='16', ef_construction='64') WHERE (embedding_dimension = " +
		strconv.Itoa(dimension) + ")"
}

type fakeIndexSession struct {
	version       string
	versionErr    error
	listErr       error
	indexes       []indexRecord
	createErr     error
	createDef     func(name string, dimension int) string
	dropErr       map[string]error
	configured    []IndexOptions
	created       []string
	dropped       []string
	sizes         map[string]int64
	configureErr  error
	indexSizeErr  error
	dropCalls     []string
	createCalls   []string
	calls         []string
	lastOptions   IndexOptions
	lastDimension int
}

func (f *fakeIndexSession) record(call string) {
	f.calls = append(f.calls, call)
}

func (f *fakeIndexSession) PgvectorVersion(context.Context) (string, error) {
	f.record("version")
	return f.version, f.versionErr
}

func (f *fakeIndexSession) ChunkIndexes(context.Context) ([]indexRecord, error) {
	f.record("list")
	return append([]indexRecord(nil), f.indexes...), f.listErr
}

func (f *fakeIndexSession) Configure(_ context.Context, options IndexOptions) error {
	f.record("configure")
	f.configured = append(f.configured, options)
	f.lastOptions = options
	return f.configureErr
}

func (f *fakeIndexSession) CreateIndex(_ context.Context, name string, dimension int) error {
	f.record("create")
	f.createCalls = append(f.createCalls, name)
	f.lastDimension = dimension
	definition := indexDefinition(name, dimension)
	if f.createDef != nil {
		definition = f.createDef(name, dimension)
	}
	if f.createErr != nil {
		// PostgreSQL leaves an INVALID index behind when a CONCURRENTLY build
		// fails mid-way.
		f.indexes = append(f.indexes, indexRecord{Name: name, Valid: false, Definition: definition})
		return f.createErr
	}
	f.created = append(f.created, name)
	f.indexes = append(f.indexes, indexRecord{Name: name, Valid: true, Definition: definition})
	return nil
}

func (f *fakeIndexSession) DropIndex(_ context.Context, name string) error {
	f.record("drop")
	f.dropCalls = append(f.dropCalls, name)
	if err := f.dropErr[name]; err != nil {
		return err
	}
	f.dropped = append(f.dropped, name)
	kept := f.indexes[:0]
	for _, record := range f.indexes {
		if record.Name != name {
			kept = append(kept, record)
		}
	}
	f.indexes = kept
	return nil
}

func (f *fakeIndexSession) IndexSizeBytes(_ context.Context, name string) (int64, error) {
	if f.indexSizeErr != nil {
		return 0, f.indexSizeErr
	}
	return f.sizes[name], nil
}

func TestEnsureIndexNoOpWhenMatchingValidIndexExists(t *testing.T) {
	session := &fakeIndexSession{
		version: "0.8.7",
		indexes: []indexRecord{{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 3)}},
	}
	result, err := ensureIndex(context.Background(), session, 3, IndexOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Created || result.Planned {
		t.Fatalf("an existing matching index must be a no-op: %+v", result)
	}
	if result.Index != ChunksIndexName {
		t.Fatalf("expected serving index %s, got %s", ChunksIndexName, result.Index)
	}
	if len(session.created) != 0 || len(session.dropped) != 0 {
		t.Fatalf("no-op must not create or drop indexes: created=%v dropped=%v", session.created, session.dropped)
	}
	if len(session.configured) != 1 {
		t.Fatalf("a real run must apply the session timeouts, got %d configure calls", len(session.configured))
	}
}

func TestEnsureIndexCreatesVersionedIndexAndDropsOld(t *testing.T) {
	session := &fakeIndexSession{
		version: "0.8.7",
		indexes: []indexRecord{{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 3)}},
		sizes:   map[string]int64{"chunks_vector_idx_4_v1": 2048},
	}
	result, err := ensureIndex(context.Background(), session, 4, IndexOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Created {
		t.Fatalf("expected a created index: %+v", result)
	}
	if result.Index != "chunks_vector_idx_4_v1" {
		t.Fatalf("unexpected index name %s", result.Index)
	}
	if len(result.Dropped) != 1 || result.Dropped[0] != ChunksIndexName {
		t.Fatalf("the legacy index must be dropped after the new one is valid: %v", result.Dropped)
	}
	if result.SizeBytes != 2048 {
		t.Fatalf("expected the built index size to be reported, got %d", result.SizeBytes)
	}
}

func TestEnsureIndexKeepsIndexForDimensionWithChunks(t *testing.T) {
	session := &fakeIndexSession{
		version: "0.8.7",
		indexes: []indexRecord{{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 3)}},
	}
	result, err := ensureIndex(context.Background(), session, 4, IndexOptions{KeepDimensions: map[int]bool{3: true}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Created || len(result.Dropped) != 0 {
		t.Fatalf("the old dimension still has chunks and must stay: created=%v dropped=%v", result.Created, result.Dropped)
	}
	if len(session.dropped) != 0 {
		t.Fatalf("dropped %v, want none", session.dropped)
	}

	// Once reembed has moved the last chunk off dimension 3, a later run may
	// prune the old index while keeping the target.
	session = &fakeIndexSession{
		version: "0.8.7",
		indexes: []indexRecord{
			{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 3)},
			{Name: "chunks_vector_idx_4_v1", Valid: true, Definition: indexDefinition("chunks_vector_idx_4_v1", 4)},
		},
	}
	result, err = ensureIndex(context.Background(), session, 4, IndexOptions{KeepDimensions: map[int]bool{4: true}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Dropped) != 1 || result.Dropped[0] != ChunksIndexName {
		t.Fatalf("the stale dimension index must be pruned once empty: %v", result.Dropped)
	}
}

func TestEnsureIndexBuildStatementIsConcurrentAndPartial(t *testing.T) {
	statement := buildChunksIndexStatement("chunks_vector_idx_4_v1", 4)
	for _, fragment := range []string{
		"CREATE INDEX CONCURRENTLY",
		`"chunks_vector_idx_4_v1"`,
		"USING hnsw",
		"halfvec(4)",
		"WHERE embedding_dimension = 4",
	} {
		if !strings.Contains(statement, fragment) {
			t.Fatalf("build statement is missing %q: %s", fragment, statement)
		}
	}
}

func TestEnsureIndexCleansInvalidResidueBeforeBuilding(t *testing.T) {
	session := &fakeIndexSession{
		version: "0.8.7",
		indexes: []indexRecord{
			{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 3)},
			{Name: "chunks_vector_idx_4_v1", Valid: false, Definition: indexDefinition("chunks_vector_idx_4_v1", 4)},
		},
	}
	result, err := ensureIndex(context.Background(), session, 4, IndexOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.dropCalls[0] != "chunks_vector_idx_4_v1" {
		t.Fatalf("the invalid residue must be dropped first, got %v", session.dropCalls)
	}
	if result.Index != "chunks_vector_idx_4_v1" {
		t.Fatalf("a cleaned version number may be reused, got %s", result.Index)
	}
	if len(result.Dropped) != 1 || result.Dropped[0] != ChunksIndexName {
		t.Fatalf("expected only the superseded serving index in Dropped, got %v", result.Dropped)
	}
}

func TestEnsureIndexKeepsServingIndexWhenBuildFails(t *testing.T) {
	session := &fakeIndexSession{
		version:   "0.8.7",
		indexes:   []indexRecord{{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 3)}},
		createErr: errors.New("lock timeout"),
	}
	_, err := ensureIndex(context.Background(), session, 4, IndexOptions{})
	if err == nil || !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("expected the build error to surface, got %v", err)
	}
	if len(session.dropped) != 1 || session.dropped[0] != "chunks_vector_idx_4_v1" {
		t.Fatalf("the invalid candidate must be cleaned up, got %v", session.dropped)
	}
	for _, record := range session.indexes {
		if record.Name == ChunksIndexName && record.Valid {
			return
		}
	}
	t.Fatalf("the serving index must survive a failed rebuild: %v", session.indexes)
}

func TestEnsureIndexRejectsOldPgvector(t *testing.T) {
	session := &fakeIndexSession{version: "0.6.2"}
	_, err := ensureIndex(context.Background(), session, 4, IndexOptions{})
	if err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("expected a version error, got %v", err)
	}
	if len(session.configured) != 0 || len(session.createCalls) != 0 {
		t.Fatalf("an unsupported pgvector must fail before touching the catalog")
	}
}

func TestEnsureIndexDryRunDoesNotTouchTheCatalog(t *testing.T) {
	session := &fakeIndexSession{
		version: "0.8.7",
		indexes: []indexRecord{{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 3)}},
	}
	result, err := ensureIndex(context.Background(), session, 4, IndexOptions{DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Planned || result.Index != "chunks_vector_idx_4_v1" {
		t.Fatalf("unexpected dry-run plan: %+v", result)
	}
	if len(result.Dropped) != 1 || result.Dropped[0] != ChunksIndexName {
		t.Fatalf("dry run must report the index it would replace: %v", result.Dropped)
	}
	if len(session.configured) != 1 {
		t.Fatalf("a dry run must still bound its catalog read with session timeouts, got %d configure calls", len(session.configured))
	}
	if len(session.createCalls) != 0 || len(session.dropCalls) != 0 {
		t.Fatalf("dry run must not modify the catalog: created=%v dropped=%v", session.createCalls, session.dropCalls)
	}
}

func TestEnsureIndexAppliesConfiguredTimeouts(t *testing.T) {
	session := &fakeIndexSession{
		version: "0.8.7",
		indexes: []indexRecord{{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 3)}},
	}
	options := IndexOptions{LockTimeout: 7 * time.Second, StatementTimeout: 2 * time.Minute}
	if _, err := ensureIndex(context.Background(), session, 4, options); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.lastOptions.LockTimeout != 7*time.Second || session.lastOptions.StatementTimeout != 2*time.Minute {
		t.Fatalf("timeouts were not passed to the session: %+v", session.lastOptions)
	}
}

func TestEnsureIndexAppliesTimeoutsBeforeReadingTheCatalog(t *testing.T) {
	session := &fakeIndexSession{
		version: "0.8.7",
		indexes: []indexRecord{{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 3)}},
	}
	if _, err := ensureIndex(context.Background(), session, 4, IndexOptions{LockTimeout: time.Second}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(session.calls) < 2 || session.calls[0] != "version" || session.calls[1] != "configure" {
		t.Fatalf("timeouts must be applied before the catalog read: %v", session.calls)
	}
	if session.calls[2] != "list" {
		t.Fatalf("the catalog read must follow the session timeouts: %v", session.calls)
	}
}

func TestEnsureIndexStopsWhenSessionConfigurationFails(t *testing.T) {
	session := &fakeIndexSession{
		version:      "0.8.7",
		indexes:      []indexRecord{{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 3)}},
		configureErr: errors.New("SET failed"),
	}
	_, err := ensureIndex(context.Background(), session, 4, IndexOptions{})
	if err == nil || !strings.Contains(err.Error(), "SET failed") {
		t.Fatalf("expected the configuration error to surface, got %v", err)
	}
	for _, call := range session.calls {
		if call != "version" && call != "configure" {
			t.Fatalf("a session that cannot apply timeouts must not touch the catalog: %v", session.calls)
		}
	}
}

func TestEnsureIndexRejectsDefinitionThatDoesNotMatchDimension(t *testing.T) {
	session := &fakeIndexSession{
		version: "0.8.7",
		indexes: []indexRecord{{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 3)}},
		createDef: func(name string, dimension int) string {
			return indexDefinition(name, dimension+1)
		},
	}
	_, err := ensureIndex(context.Background(), session, 4, IndexOptions{})
	if err == nil || !strings.Contains(err.Error(), "did not validate") {
		t.Fatalf("expected a validation error, got %v", err)
	}
	for _, call := range session.dropCalls {
		if call == "chunks_vector_idx_4_v1" {
			return
		}
	}
	t.Fatalf("a mismatched index must be dropped: %v", session.dropCalls)
}

func TestEnsureIndexDropsStaleValidIndexOnNoOp(t *testing.T) {
	session := &fakeIndexSession{
		version: "0.8.7",
		indexes: []indexRecord{
			{Name: "chunks_vector_idx_4_v1", Valid: true, Definition: indexDefinition("chunks_vector_idx_4_v1", 4)},
			{Name: ChunksIndexName, Valid: true, Definition: indexDefinition(ChunksIndexName, 3)},
		},
	}
	result, err := ensureIndex(context.Background(), session, 4, IndexOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Created {
		t.Fatalf("a matching index must not be rebuilt")
	}
	if len(result.Dropped) != 1 || result.Dropped[0] != ChunksIndexName {
		t.Fatalf("a stale dimension index must still be removed: %v", result.Dropped)
	}
}

func TestNextChunksIndexNameUsesHighestVersionForDimension(t *testing.T) {
	records := []indexRecord{
		{Name: "chunks_vector_idx_4_v1", Valid: true},
		{Name: "chunks_vector_idx_4_v3", Valid: true},
		{Name: "chunks_vector_idx_8_v9", Valid: true},
		{Name: "chunks_vector_idx_4_v4", Valid: false},
		{Name: ChunksIndexName, Valid: true},
	}
	if got := nextChunksIndexName(records, 4); got != "chunks_vector_idx_4_v4" {
		t.Fatalf("unexpected next name %s", got)
	}
	if got := nextChunksIndexName(records, 16); got != "chunks_vector_idx_16_v1" {
		t.Fatalf("unexpected first name for a new dimension: %s", got)
	}
}

func TestParseChunksIndexName(t *testing.T) {
	dimension, version, ok := parseChunksIndexName("chunks_vector_idx_1024_v12")
	if !ok || dimension != 1024 || version != 12 {
		t.Fatalf("unexpected parse result: %d %d %t", dimension, version, ok)
	}
	if _, _, ok := parseChunksIndexName("chunks_vector_idx"); ok {
		t.Fatalf("the legacy unversioned name has no version")
	}
	if _, _, ok := parseChunksIndexName("chunks_vector_idx_1024"); ok {
		t.Fatalf("a name without a version suffix must not parse")
	}
}

func TestCheckPgvectorVersion(t *testing.T) {
	cases := []struct {
		version string
		wantErr bool
	}{
		{"0.7.0", false},
		{"0.7.4", false},
		{"0.8.7", false},
		{"1.0.0", false},
		{"0.6.2", true},
		{"nonsense", true},
		{"0.7", false},
	}
	for _, testCase := range cases {
		err := checkPgvectorVersion(testCase.version)
		if testCase.wantErr && err == nil {
			t.Fatalf("version %s must be rejected", testCase.version)
		}
		if !testCase.wantErr && err != nil {
			t.Fatalf("version %s must be accepted: %v", testCase.version, err)
		}
	}
}

func TestEnsureIndexRejectsNonPositiveDimension(t *testing.T) {
	session := &fakeIndexSession{version: "0.8.7"}
	if _, err := ensureIndex(context.Background(), session, 0, IndexOptions{}); err == nil {
		t.Fatalf("expected an error for dimension 0")
	}
}

func TestDimensionFromIndexDefParsesHalfvecDimension(t *testing.T) {
	definition := "CREATE INDEX chunks_vector_idx ON public.chunks USING hnsw (((embedding)::halfvec(768)) halfvec_cosine_ops) WITH (m='16', ef_construction='64') WHERE (embedding_dimension = 768)"
	dimension, err := dimensionFromIndexDef(definition)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dimension != 768 {
		t.Fatalf("expected 768, got %d", dimension)
	}
}

func TestDimensionFromIndexDefRejectsMissingHalfvec(t *testing.T) {
	if _, err := dimensionFromIndexDef("CREATE INDEX chunks_vector_idx ON public.chunks USING bm25 (id)"); err == nil {
		t.Fatalf("expected error for definition without halfvec dimension")
	}
}

func TestDimensionFromIndexDefFallsBackToPartialPredicate(t *testing.T) {
	definition := "CREATE INDEX chunks_vector_idx_4_v1 ON public.chunks USING hnsw (embedding halfvec_cosine_ops) WITH (m='16', ef_construction='64') WHERE (embedding_dimension = 4)"
	dimension, err := dimensionFromIndexDef(definition)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dimension != 4 {
		t.Fatalf("expected 4 from the partial predicate, got %d", dimension)
	}
}

func TestIndexCoversDimensionDoesNotMatchPrefix(t *testing.T) {
	if indexCoversDimension("... WHERE (embedding_dimension = 40)", 4) {
		t.Fatalf("dimension 4 must not match a predicate for 40")
	}
	if !indexCoversDimension("... WHERE (embedding_dimension = 4)", 4) {
		t.Fatalf("dimension 4 must match its own predicate")
	}
}
