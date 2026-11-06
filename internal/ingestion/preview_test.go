package ingestion

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/example/rag-platform/internal/authz"
	"github.com/example/rag-platform/internal/platform/persistence/models"
)

func TestDocumentCursorRoundTrips(t *testing.T) {
	createdAt := time.Date(2026, 10, 16, 8, 44, 5, 658854000, time.UTC)
	id := "doc-0ea548408ff2911a02efd368"

	cursor, err := decodeDocumentCursor(encodeDocumentCursor(createdAt, id))
	if err != nil {
		t.Fatalf("expected the encoded cursor to decode: %v", err)
	}
	if !cursor.CreatedAt.Equal(createdAt) {
		t.Fatalf("expected created_at %s, got %s", createdAt, cursor.CreatedAt)
	}
	if cursor.ID != id {
		t.Fatalf("expected id %s, got %s", id, cursor.ID)
	}
}

func TestDocumentCursorPreservesMicrosecondPrecision(t *testing.T) {
	// PostgreSQL stores timestamptz at microsecond precision, so a cursor that
	// survives the round trip must compare equal in the keyset query.
	createdAt := time.Date(2026, 10, 16, 8, 44, 5, 123456000, time.UTC)
	cursor, err := decodeDocumentCursor(encodeDocumentCursor(createdAt, "doc-abc"))
	if err != nil {
		t.Fatalf("expected the encoded cursor to decode: %v", err)
	}
	if !cursor.CreatedAt.Equal(createdAt) {
		t.Fatalf("expected %s to survive the round trip, got %s", createdAt, cursor.CreatedAt)
	}
}

func TestDecodeDocumentCursorRejectsMalformedInput(t *testing.T) {
	for _, raw := range []string{"", "-", "-doc-1", "123-", "not-a-number-id", "abc-doc-1"} {
		if _, err := decodeDocumentCursor(raw); err == nil {
			t.Fatalf("expected %q to be rejected as a cursor", raw)
		}
	}
}

func TestClampPageLimitFallsBackOutOfRange(t *testing.T) {
	cases := map[int]int{0: defaultPageLimit, -1: defaultPageLimit, 1: 1, 51: 51, maxPageLimit: maxPageLimit, maxPageLimit + 1: defaultPageLimit, 10000: defaultPageLimit}
	for input, expected := range cases {
		if got := clampPageLimit(input); got != expected {
			t.Fatalf("clampPageLimit(%d) = %d, want %d", input, got, expected)
		}
	}
}

func TestChunksCursorAdvancesFromZeroOrdinal(t *testing.T) {
	service, db, _ := newReparseFixture(t)
	now := time.Now()
	chunks := []models.Chunk{
		{ID: "chunk-0", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentVersionID: "ver-1", ParentID: "parent-0", Ordinal: 0, Content: "zero", ContentHash: "h0", Status: StatusEvidenceReady, CreatedAt: now, UpdatedAt: now},
		{ID: "chunk-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentVersionID: "ver-1", ParentID: "parent-1", Ordinal: 1, Content: "one", ContentHash: "h1", Status: StatusEvidenceReady, CreatedAt: now, UpdatedAt: now},
		{ID: "chunk-2", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", DocumentVersionID: "ver-1", ParentID: "parent-2", Ordinal: 2, Content: "two", ContentHash: "h2", Status: StatusEvidenceReady, CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(&chunks).Error; err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	principal := authz.Principal{SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session"}
	ctx := context.Background()

	page, err := service.Chunks(ctx, principal, "doc-a", 0, 1)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if !page.HasMore || page.NextCursor != 1 || len(page.Items) != 1 || page.Items[0].Ordinal != 0 {
		t.Fatalf("first page = %+v, want ordinal 0 and next cursor 1", page)
	}
	page, err = service.Chunks(ctx, principal, "doc-a", page.NextCursor, 1)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if !page.HasMore || page.NextCursor != 2 || len(page.Items) != 1 || page.Items[0].Ordinal != 1 {
		t.Fatalf("second page = %+v, want ordinal 1 and next cursor 2", page)
	}
	page, err = service.Chunks(ctx, principal, "doc-a", page.NextCursor, 1)
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if page.HasMore || len(page.Items) != 1 || page.Items[0].Ordinal != 2 {
		t.Fatalf("third page = %+v, want final ordinal 2", page)
	}
}

func TestSourceDocumentStreamsSourceObject(t *testing.T) {
	service, _, objects := newReparseFixture(t)
	source, err := service.SourceDocument(context.Background(), authz.Principal{
		SubjectID: "user-a", TenantID: "tenant-a", CredentialType: "session",
	}, "doc-a")
	if err != nil {
		t.Fatalf("source document: %v", err)
	}
	defer source.Body.Close()
	content, err := io.ReadAll(source.Body)
	if err != nil {
		t.Fatalf("read source body: %v", err)
	}
	if got, want := string(content), "reparse idempotency fixture"; got != want {
		t.Fatalf("source body = %q, want %q", got, want)
	}
	if source.Size != int64(len(content)) {
		t.Fatalf("source size = %d, want %d", source.Size, len(content))
	}
	gets, opens := objects.counts()
	if gets != 0 || opens != 1 {
		t.Fatalf("object reads: Get=%d Open=%d, want Get=0 Open=1", gets, opens)
	}
}
