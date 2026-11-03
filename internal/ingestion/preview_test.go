package ingestion

import (
	"testing"
	"time"
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
