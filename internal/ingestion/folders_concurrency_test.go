package ingestion

import (
	"context"
	"strings"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
)

// Two moves that would each put the other folder inside its own subtree must
// serialize: exactly one succeeds and the tree stays acyclic.
func TestFolderConcurrentMovesCannotFormCycle(t *testing.T) {
	service, principal := newPreviewFixture(t)
	ctx := context.Background()
	owner := principal()

	a, err := service.CreateFolder(ctx, owner, "kb-a", "A", "")
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	b, err := service.CreateFolder(ctx, owner, "kb-a", "B", "")
	if err != nil {
		t.Fatalf("create B: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	move := func(folderID, parentID string) {
		<-start
		_, moveErr := service.UpdateFolder(ctx, owner, folderID, FolderUpdate{ParentID: &parentID})
		errs <- moveErr
	}
	go move(a.ID, b.ID)
	go move(b.ID, a.ID)
	close(start)

	first, second := <-errs, <-errs
	failures := 0
	for _, moveErr := range []error{first, second} {
		if moveErr != nil {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("expected exactly one rejected move, got %d (first=%v second=%v)", failures, first, second)
	}

	var folders []models.Folder
	if err := service.db.Where("knowledge_base_id = ?", "kb-a").Find(&folders).Error; err != nil {
		t.Fatalf("load folders: %v", err)
	}
	byID := make(map[string]models.Folder, len(folders))
	for _, folder := range folders {
		byID[folder.ID] = folder
	}
	for _, folder := range folders {
		if folder.ParentID == nil {
			continue
		}
		parent, ok := byID[*folder.ParentID]
		if !ok {
			t.Fatalf("folder %s points at missing parent %s", folder.ID, *folder.ParentID)
		}
		if !strings.HasPrefix(folder.Path, parent.Path) {
			t.Fatalf("folder %s path %q is not under parent %q", folder.ID, folder.Path, parent.Path)
		}
		if strings.HasPrefix(parent.Path, folder.Path) {
			t.Fatalf("cycle: %s (%q) and %s (%q)", folder.ID, folder.Path, parent.ID, parent.Path)
		}
	}
}
