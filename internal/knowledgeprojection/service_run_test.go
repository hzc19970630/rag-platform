package knowledgeprojection

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/contracts/evidence"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type recordingEvidence struct {
	visited []string
}

func (r *recordingEvidence) CurrentPackages(_ context.Context, _, knowledgeBaseID string) ([]evidence.Package, error) {
	r.visited = append(r.visited, knowledgeBaseID)
	return nil, nil
}

func newSweepFixture(t *testing.T, count int, sameTimestamp bool) (*Service, *recordingEvidence) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("test db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.KnowledgeBase{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("kb-%02d", index)
		updatedAt := base.Add(time.Duration(index) * time.Minute)
		if sameTimestamp {
			updatedAt = base
		}
		knowledgeBase := models.KnowledgeBase{
			ID: id, TenantID: "t1", OwnerID: "u1", Name: id,
			IndexingStrategy: `{"wiki_enabled":true}`,
			CreatedAt:        updatedAt, UpdatedAt: updatedAt,
		}
		if err := db.Create(&knowledgeBase).Error; err != nil {
			t.Fatalf("create knowledge base %s: %v", id, err)
		}
	}
	recorder := &recordingEvidence{}
	return NewService(Options{DB: db, Evidence: recorder}), recorder
}

func TestScheduleMissingPagesThroughEveryKnowledgeBase(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		sameTimestamp bool
	}{
		{name: "distinct timestamps"},
		{name: "identical timestamps need the id tie-break", sameTimestamp: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service, recorder := newSweepFixture(t, 25, testCase.sameTimestamp)
			ctx := context.Background()
			cursor := ""
			var pages [][]string
			for page := 0; page < 3; page++ {
				before := len(recorder.visited)
				started, next, err := service.ScheduleMissing(ctx, 10, cursor)
				if err != nil {
					t.Fatalf("page %d: %v", page+1, err)
				}
				if started != 0 {
					t.Fatalf("page %d started %d projections without evidence packages", page+1, started)
				}
				pages = append(pages, append([]string(nil), recorder.visited[before:]...))
				cursor = next
				if page < 2 && next == "" {
					t.Fatalf("page %d did not hand out a follow-up cursor", page+1)
				}
				if page == 2 && next != "" {
					t.Fatalf("exhausted sweep kept cursor %q", next)
				}
			}
			for index, want := range []int{10, 10, 5} {
				if len(pages[index]) != want {
					t.Fatalf("page %d visited %d knowledge bases, want %d: %v", index+1, len(pages[index]), want, pages[index])
				}
			}
			seen := make(map[string]bool, 25)
			for _, page := range pages {
				for _, id := range page {
					if seen[id] {
						t.Fatalf("knowledge base %s was scheduled twice within one sweep", id)
					}
					seen[id] = true
				}
			}
			if len(seen) != 25 {
				t.Fatalf("sweep covered %d of 25 knowledge bases", len(seen))
			}

			before := len(recorder.visited)
			if _, next, err := service.ScheduleMissing(ctx, 10, ""); err != nil {
				t.Fatalf("restart sweep: %v", err)
			} else if next == "" {
				t.Fatal("restarted sweep is expected to hand out a cursor for a 25-row backlog")
			}
			if got := len(recorder.visited) - before; got != 10 {
				t.Fatalf("restarted sweep visited %d knowledge bases, want the first page of 10", got)
			}
		})
	}
}

func TestScheduleMissingRejectsMalformedCursor(t *testing.T) {
	service, _ := newSweepFixture(t, 1, false)
	for _, cursor := range []string{"no-separator", "-missing-nanos", "not-a-number-id", "123-"} {
		if _, _, err := service.ScheduleMissing(context.Background(), 10, cursor); err == nil {
			t.Fatalf("cursor %q should be rejected", cursor)
		}
	}
}
