package datasource

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

func TestCursorRoundTripAndDefaults(t *testing.T) {
	state := cursorState{ManifestHash: "abc", Position: 7, Phase: cursorPhaseTombstones}
	decoded := decodeCursor(encodeCursor(state))
	if decoded != state {
		t.Fatalf("cursor round trip: %+v != %+v", decoded, state)
	}
	if got := decodeCursor(""); got.Phase != cursorPhaseItems || got.Position != 0 {
		t.Fatalf("empty cursor must start the item phase: %+v", got)
	}
	if got := decodeCursor("not-json"); got.Phase != cursorPhaseItems {
		t.Fatalf("invalid cursor must restart safely: %+v", got)
	}
	if got := decodeCursor(`{"position":3}`); got.Phase != cursorPhaseItems || got.Position != 3 {
		t.Fatalf("missing phase must default to items: %+v", got)
	}
}

func TestUnchangedByMetadataUsesVersionAndChecksum(t *testing.T) {
	mapping := models.ConnectorItem{RemoteVersion: "v1", ContentHash: strings.Repeat("a", 64)}
	if !unchangedByMetadata(mapping, ManifestItem{Version: "v1"}) {
		t.Fatalf("matching version must skip the download")
	}
	if !unchangedByMetadata(mapping, ManifestItem{SHA256: strings.Repeat("a", 64)}) {
		t.Fatalf("matching checksum must skip the download")
	}
	if unchangedByMetadata(mapping, ManifestItem{Version: "v2"}) {
		t.Fatalf("changed version must not skip")
	}
	if unchangedByMetadata(mapping, ManifestItem{SHA256: strings.Repeat("b", 64)}) {
		t.Fatalf("changed checksum must not skip")
	}
	if unchangedByMetadata(models.ConnectorItem{}, ManifestItem{}) {
		t.Fatalf("no remote change token cannot prove an item unchanged")
	}
}

func TestSupportedFilename(t *testing.T) {
	for _, name := range []string{"a.pdf", "A.DOCX", "notes.md", "readme.txt", "report.xlsx", "deck.pptx", "page.html", "page.htm", "Page.HTM", "PAGE.XLSX"} {
		if !supportedFilename(name) {
			t.Fatalf("expected %q to be supported", name)
		}
	}
	for _, name := range []string{"", "image.png", "archive.zip", "noext", "legacy.xls", "legacy.ppt"} {
		if supportedFilename(name) {
			t.Fatalf("expected %q to be rejected", name)
		}
	}
}

func TestTerminalIngestError(t *testing.T) {
	terminal := []error{
		apperror.ErrQuotaExceeded,
		fmt.Errorf("wrapped: %w", apperror.ErrNotFound),
		fmt.Errorf("wrapped: %w", apperror.ErrForbidden),
		fmt.Errorf("unsupported mime type: image/png"),
		fmt.Errorf("file exceeds maximum size"),
		fmt.Errorf("filename is required"),
		fmt.Errorf("chunks must not be empty"),
	}
	for _, err := range terminal {
		if !terminalIngestError(err) {
			t.Fatalf("expected terminal: %v", err)
		}
	}
	for _, err := range []error{fmt.Errorf("dial tcp: connection refused"), fmt.Errorf("object storage unavailable")} {
		if terminalIngestError(err) {
			t.Fatalf("infrastructure error must be retried: %v", err)
		}
	}
}

func TestAppendSummaryCapsLength(t *testing.T) {
	summary := ""
	for index := 0; index < 500; index++ {
		summary = appendSummary(summary, fmt.Sprintf("item-%d", index), fmt.Errorf("failed"))
	}
	if len(summary) != 4000 {
		t.Fatalf("summary must be capped at 4000 characters, got %d", len(summary))
	}
	if !strings.Contains(summary, "item-0: failed") {
		t.Fatalf("summary must retain earlier failures: %q", summary[:80])
	}
	if got := appendSummary("", "a", fmt.Errorf("boom")); got != "a: boom" {
		t.Fatalf("unexpected summary line: %q", got)
	}
}

func TestCountersAndRunStatus(t *testing.T) {
	run := models.SyncRun{ItemsTotal: 5, ItemsCreated: 1, ItemsUpdated: 2, ItemsDeleted: 3, ItemsSkipped: 4, ItemsFailed: 5}
	counters := countersFrom(run)
	if counters != (syncCounters{total: 5, created: 1, updated: 2, deleted: 3, skipped: 4, failed: 5}) {
		t.Fatalf("unexpected counters: %+v", counters)
	}
	for _, status := range []string{StatusPending, StatusRunning} {
		if !runActive(status) {
			t.Fatalf("%s must be active", status)
		}
	}
	for _, status := range []string{StatusSucceeded, StatusPartial, StatusFailed, StatusCancelled, ""} {
		if runActive(status) {
			t.Fatalf("%q must not be active", status)
		}
	}
}

func TestRetryDelayHonorsRetryAfterWithCap(t *testing.T) {
	if got := retryDelay(0, "2"); got.Seconds() != 2 {
		t.Fatalf("retry-after not honored: %v", got)
	}
	if got := retryDelay(0, "3600"); got.Seconds() != 30 {
		t.Fatalf("retry-after must be capped at 30s: %v", got)
	}
	if got := retryDelay(2, ""); got.Milliseconds() != 2000 {
		t.Fatalf("unexpected backoff: %v", got)
	}
	if got := retryDelay(0, "-5"); got.Milliseconds() != 500 {
		t.Fatalf("invalid retry-after must fall back to backoff: %v", got)
	}
}
