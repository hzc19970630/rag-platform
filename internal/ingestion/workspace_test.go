package ingestion

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hzc19970630/rag-platform/internal/apperror"
)

func TestFolderTreeLifecycle(t *testing.T) {
	service, principal := newPreviewFixture(t)
	ctx := context.Background()
	owner := principal()

	root, err := service.CreateFolder(ctx, owner, "kb-a", "Contracts", "")
	if err != nil {
		t.Fatalf("create root folder: %v", err)
	}
	if root.Depth != 1 || root.Path != "/"+root.ID+"/" {
		t.Fatalf("unexpected root folder %+v", root)
	}
	child, err := service.CreateFolder(ctx, owner, "kb-a", "2026", root.ID)
	if err != nil {
		t.Fatalf("create child folder: %v", err)
	}
	if child.Depth != 2 || child.Path != root.Path+child.ID+"/" {
		t.Fatalf("unexpected child folder %+v", child)
	}
	if _, err := service.CreateFolder(ctx, owner, "kb-a", "contracts", ""); err == nil {
		t.Fatal("duplicate sibling name must be rejected")
	}

	sibling, err := service.CreateFolder(ctx, owner, "kb-a", "Archive", "")
	if err != nil {
		t.Fatalf("create sibling folder: %v", err)
	}
	moved, err := service.UpdateFolder(ctx, owner, sibling.ID, FolderUpdate{ParentID: &root.ID})
	if err != nil {
		t.Fatalf("move folder under root: %v", err)
	}
	if moved.Depth != 2 || moved.Path != root.Path+moved.ID+"/" {
		t.Fatalf("unexpected moved folder %+v", moved)
	}
	if _, err := service.UpdateFolder(ctx, owner, root.ID, FolderUpdate{ParentID: &moved.ID}); err == nil {
		t.Fatal("moving a folder into its own subtree must be rejected")
	}
	if err := service.DeleteFolder(ctx, owner, root.ID); err == nil {
		t.Fatal("deleting a non-empty folder must be rejected")
	}
	if err := service.DeleteFolder(ctx, owner, moved.ID); err != nil {
		t.Fatalf("delete empty folder: %v", err)
	}
	folders, err := service.ListFolders(ctx, owner, "kb-a")
	if err != nil || len(folders) != 2 {
		t.Fatalf("ListFolders = %d, %v", len(folders), err)
	}
}

func TestFolderDepthLimit(t *testing.T) {
	service, principal := newPreviewFixture(t)
	ctx := context.Background()
	owner := principal()
	parent := ""
	for depth := 1; depth <= maxFolderDepth; depth++ {
		folder, err := service.CreateFolder(ctx, owner, "kb-a", fmt.Sprintf("level-%d", depth), parent)
		if err != nil {
			t.Fatalf("create depth %d: %v", depth, err)
		}
		parent = folder.ID
	}
	if _, err := service.CreateFolder(ctx, owner, "kb-a", "too-deep", parent); err == nil {
		t.Fatal("folder deeper than the limit must be rejected")
	}
}

func TestTagsFilterAndCascade(t *testing.T) {
	service, principal := newPreviewFixture(t)
	ctx := context.Background()
	owner := principal()

	first, err := service.CreateTag(ctx, owner, "kb-a", "Finance")
	if err != nil {
		t.Fatalf("create tag: %v", err)
	}
	if _, err := service.CreateTag(ctx, owner, "kb-a", "finance"); err == nil {
		t.Fatal("duplicate tag name must be rejected")
	}
	second, err := service.CreateTag(ctx, owner, "kb-a", "Draft")
	if err != nil {
		t.Fatalf("create second tag: %v", err)
	}
	results, err := service.BatchDocuments(ctx, owner, "kb-a", BatchInput{
		Action: BatchTag, DocumentIDs: []string{"doc-a"}, TagIDs: []string{first.ID, second.ID},
	})
	if err != nil || len(results) != 1 || !results[0].OK {
		t.Fatalf("tag document = %+v, %v", results, err)
	}
	page, err := service.ListDocuments(ctx, owner, "kb-a", DocumentFilter{TagIDs: []string{first.ID, second.ID}}, "", 0)
	if err != nil || len(page.Items) != 1 || len(page.Items[0].Tags) != 2 {
		t.Fatalf("AND tag filter = %+v, %v", page, err)
	}
	if _, err := service.ListDocuments(ctx, owner, "kb-a", DocumentFilter{TagIDs: []string{"tag-missing"}}, "", 0); !errors.Is(err, apperror.ErrNotFound) {
		t.Fatalf("unknown tag filter must be not_found, got %v", err)
	}
	if _, err := service.BatchDocuments(ctx, owner, "kb-a", BatchInput{
		Action: BatchUntag, DocumentIDs: []string{"doc-a"}, TagIDs: []string{first.ID},
	}); err != nil {
		t.Fatalf("untag document: %v", err)
	}
	page, err = service.ListDocuments(ctx, owner, "kb-a", DocumentFilter{TagIDs: []string{first.ID, second.ID}}, "", 0)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("untagged document must not match AND filter: %+v, %v", page, err)
	}
	if err := service.DeleteTag(ctx, owner, second.ID); err != nil {
		t.Fatalf("delete tag: %v", err)
	}
	tags, err := service.ListTags(ctx, owner, "kb-a")
	if err != nil || len(tags) != 1 {
		t.Fatalf("ListTags = %+v, %v", tags, err)
	}
}

func TestBatchDocumentsMoveAndPartialFailure(t *testing.T) {
	service, principal := newPreviewFixture(t)
	ctx := context.Background()
	owner := principal()

	folder, err := service.CreateFolder(ctx, owner, "kb-a", "Inbox", "")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	results, err := service.BatchDocuments(ctx, owner, "kb-a", BatchInput{
		Action: BatchMove, DocumentIDs: []string{"doc-a", "doc-missing"}, FolderID: folder.ID,
	})
	if err != nil {
		t.Fatalf("batch move: %v", err)
	}
	if len(results) != 2 || !results[0].OK || results[1].ErrorCode != "not_found" {
		t.Fatalf("partial batch results = %+v", results)
	}
	page, err := service.ListDocuments(ctx, owner, "kb-a", DocumentFilter{FolderID: folder.ID}, "", 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].Document.ID != "doc-a" {
		t.Fatalf("folder filter = %+v, %v", page, err)
	}
	if _, err := service.BatchDocuments(ctx, owner, "kb-a", BatchInput{
		Action: BatchMove, DocumentIDs: []string{"doc-a"}, FolderID: "",
	}); err != nil {
		t.Fatalf("move to root: %v", err)
	}
	rootPage, err := service.ListDocuments(ctx, owner, "kb-a", DocumentFilter{FolderID: "root"}, "", 0)
	if err != nil || len(rootPage.Items) != 1 {
		t.Fatalf("root filter = %+v, %v", rootPage, err)
	}

	many := make([]string, maxBatchDocuments+1)
	for index := range many {
		many[index] = fmt.Sprintf("doc-%d", index)
	}
	if _, err := service.BatchDocuments(ctx, owner, "kb-a", BatchInput{Action: BatchDelete, DocumentIDs: many}); err == nil {
		t.Fatal("batch larger than the limit must be rejected")
	}
	if _, err := service.BatchDocuments(ctx, owner, "kb-a", BatchInput{Action: "unknown", DocumentIDs: []string{"doc-a"}}); err == nil {
		t.Fatal("unknown batch action must be rejected")
	}
}
