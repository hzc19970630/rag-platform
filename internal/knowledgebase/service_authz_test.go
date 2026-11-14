package knowledgebase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/authz"
	"github.com/hzc19970630/rag-platform/internal/contracts/model"
	"github.com/hzc19970630/rag-platform/internal/platform/persistence/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeEmbeddingCatalog struct {
	binding model.EmbeddingBinding
	err     error
}

func (c fakeEmbeddingCatalog) ResolveEmbeddingModel(context.Context, string, string) (model.EmbeddingBinding, error) {
	return c.binding, c.err
}

func (c fakeEmbeddingCatalog) ResolveBinding(context.Context, string, model.Capability, string) (model.ModelRef, error) {
	return model.ModelRef{Model: c.binding.Model}, c.err
}

type fakeProjectionLifecycle struct {
	invalidateErr error
}

func (p fakeProjectionLifecycle) InvalidateKnowledgeBase(context.Context, string, string, string) error {
	return p.invalidateErr
}

func (p fakeProjectionLifecycle) RebuildKnowledgeBase(context.Context, string, string) (int, error) {
	return 0, nil
}

func (p fakeProjectionLifecycle) RebuildRelations(context.Context, string, string) (int, error) {
	return 0, nil
}

func newKBFixture(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&models.KnowledgeBase{}, &models.KnowledgeBaseMember{}, &models.Membership{},
		&models.AuthorizationRevision{}, &models.AuditLog{}, &models.Document{},
	); err != nil {
		t.Fatalf("migrate knowledge base tables: %v", err)
	}
	now := time.Now()
	if err := db.Create(&[]models.Membership{
		{TenantID: "tenant-a", UserID: "owner-a", Role: "owner", CreatedAt: now, UpdatedAt: now},
		{TenantID: "tenant-a", UserID: "viewer-a", Role: "viewer", CreatedAt: now, UpdatedAt: now},
		{TenantID: "tenant-a", UserID: "member-c", Role: "member", CreatedAt: now, UpdatedAt: now},
		{TenantID: "tenant-b", UserID: "owner-b", Role: "owner", CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	if err := db.Create(&[]models.KnowledgeBase{
		{ID: "kb-a", TenantID: "tenant-a", OwnerID: "owner-a", Name: "KB A", ACLVersion: 1, EmbeddingModel: "embed", EmbeddingDimension: 8, RetrievalConfig: "{}", IndexingStrategy: "{}", CreatedAt: now, UpdatedAt: now},
		{ID: "kb-b", TenantID: "tenant-b", OwnerID: "owner-b", Name: "KB B", ACLVersion: 1, EmbeddingModel: "embed", EmbeddingDimension: 8, RetrievalConfig: "{}", IndexingStrategy: "{}", CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("seed knowledge bases: %v", err)
	}
	if err := db.Create(&models.KnowledgeBaseMember{KnowledgeBaseID: "kb-a", UserID: "viewer-a", Role: "viewer"}).Error; err != nil {
		t.Fatalf("seed knowledge base member: %v", err)
	}
	authzService, err := authz.NewService(authz.NewPostgresStores(db))
	if err != nil {
		t.Fatalf("authz service: %v", err)
	}
	return NewService(db, authzService, EmbeddingBinding{Model: "embed", Dimension: 8}), db
}

func ownerA() authz.Principal  { return authz.Principal{SubjectID: "owner-a", TenantID: "tenant-a"} }
func viewerA() authz.Principal { return authz.Principal{SubjectID: "viewer-a", TenantID: "tenant-a"} }
func memberC() authz.Principal { return authz.Principal{SubjectID: "member-c", TenantID: "tenant-a"} }

func stringPointer(value string) *string { return &value }
func intPointer(value int) *int          { return &value }

func TestCreateAndListKnowledgeBases(t *testing.T) {
	service, db := newKBFixture(t)
	ctx := context.Background()

	if _, err := service.Create(ctx, ownerA(), CreateInput{}); err == nil {
		t.Fatal("empty name must be rejected")
	}
	if _, err := service.Create(ctx, authz.Principal{SubjectID: "ghost", TenantID: "tenant-a"}, CreateInput{Name: "x"}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("ghost create = %v, want forbidden", err)
	}
	created, err := service.Create(ctx, ownerA(), CreateInput{Name: "New KB", Description: "desc"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.OwnerID != "owner-a" || created.EmbeddingModel != "embed" || created.EmbeddingDimension != 8 {
		t.Fatalf("created knowledge base = %+v", created)
	}
	var members int64
	if err := db.Model(&models.KnowledgeBaseMember{}).Where("knowledge_base_id = ? AND role = ?", created.ID, "owner").Count(&members).Error; err != nil {
		t.Fatalf("count members: %v", err)
	}
	if members != 1 {
		t.Fatalf("owner member rows = %d, want 1", members)
	}

	ownerList, err := service.List(ctx, ownerA())
	if err != nil || len(ownerList) != 2 {
		t.Fatalf("owner List = %d, %v", len(ownerList), err)
	}
	viewerList, err := service.List(ctx, viewerA())
	if err != nil || len(viewerList) != 1 || viewerList[0].ID != "kb-a" {
		t.Fatalf("viewer List = %+v, %v", viewerList, err)
	}
	emptyList, err := service.List(ctx, memberC())
	if err != nil || len(emptyList) != 0 {
		t.Fatalf("member List = %+v, %v", emptyList, err)
	}
}

func TestBackfillEmbeddingBinding(t *testing.T) {
	_, db := newKBFixture(t)
	now := time.Now()
	if err := db.Create(&models.KnowledgeBase{ID: "kb-empty", TenantID: "tenant-a", OwnerID: "owner-a", Name: "Empty", RetrievalConfig: "{}", IndexingStrategy: "{}", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed empty knowledge base: %v", err)
	}
	updated, err := BackfillEmbeddingBinding(context.Background(), db, EmbeddingBinding{Model: "embed", Dimension: 8})
	if err != nil || updated != 1 {
		t.Fatalf("BackfillEmbeddingBinding = %d, %v", updated, err)
	}
	if updated, err := BackfillEmbeddingBinding(context.Background(), db, EmbeddingBinding{}); err != nil || updated != 0 {
		t.Fatalf("empty binding backfill = %d, %v", updated, err)
	}
}

func TestGetAndUpdateKnowledgeBase(t *testing.T) {
	service, db := newKBFixture(t)
	ctx := context.Background()

	if _, err := service.Get(ctx, ownerA(), "kb-a"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := service.Get(ctx, viewerA(), "kb-a"); err != nil {
		t.Fatalf("viewer Get: %v", err)
	}
	if _, err := service.Get(ctx, ownerA(), "kb-missing"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("missing Get = %v, want forbidden", err)
	}
	if _, err := service.Update(ctx, viewerA(), "kb-a", UpdateInput{Name: stringPointer("x")}); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("viewer Update = %v, want forbidden", err)
	}
	if _, err := service.Update(ctx, ownerA(), "kb-a", UpdateInput{Name: stringPointer("  ")}); err == nil {
		t.Fatal("empty name update must be rejected")
	}
	updated, err := service.Update(ctx, ownerA(), "kb-a", UpdateInput{Name: stringPointer("Renamed"), Description: stringPointer("new desc")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Renamed" || updated.Description != "new desc" {
		t.Fatalf("updated knowledge base = %+v", updated)
	}

	if _, err := service.Update(ctx, ownerA(), "kb-a", UpdateInput{EmbeddingModel: stringPointer("embed")}); err != nil {
		t.Fatalf("same binding update: %v", err)
	}
	if _, err := service.Update(ctx, ownerA(), "kb-a", UpdateInput{EmbeddingModel: stringPointer("other")}); err == nil || !strings.Contains(err.Error(), "unsupported embedding binding") {
		t.Fatalf("unsupported model = %v", err)
	}
	if _, err := service.Update(ctx, ownerA(), "kb-a", UpdateInput{EmbeddingDimension: intPointer(16)}); err == nil || !strings.Contains(err.Error(), "unsupported embedding binding") {
		t.Fatalf("unsupported dimension = %v", err)
	}

	now := time.Now()
	if err := db.Create(&models.KnowledgeBase{ID: "kb-empty", TenantID: "tenant-a", OwnerID: "owner-a", Name: "Empty", RetrievalConfig: "{}", IndexingStrategy: "{}", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed empty knowledge base: %v", err)
	}
	adopted, err := service.Update(ctx, ownerA(), "kb-empty", UpdateInput{EmbeddingModel: stringPointer("embed")})
	if err != nil || adopted.EmbeddingModel != "embed" || adopted.EmbeddingDimension != 8 {
		t.Fatalf("adopt deployment binding = %+v, %v", adopted, err)
	}
}

func TestUpdateEmbeddingBindingWithCatalog(t *testing.T) {
	service, db := newKBFixture(t)
	ctx := context.Background()
	service.SetModelCatalog(fakeEmbeddingCatalog{binding: model.EmbeddingBinding{Model: "embed2", Dimension: 16}})

	now := time.Now()
	if err := db.Create(&models.KnowledgeBase{
		ID: "kb-c", TenantID: "tenant-a", OwnerID: "owner-a", Name: "C", ACLVersion: 1,
		EmbeddingModel: "embed", EmbeddingDimension: 8, RetrievalConfig: "{}", IndexingStrategy: "{}",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed knowledge base: %v", err)
	}
	updated, err := service.Update(ctx, ownerA(), "kb-c", UpdateInput{EmbeddingModel: stringPointer("embed2")})
	if err != nil {
		t.Fatalf("catalog update: %v", err)
	}
	if updated.EmbeddingModel != "embed2" || updated.EmbeddingDimension != 16 {
		t.Fatalf("catalog binding = %+v", updated)
	}
	if _, err := service.Update(ctx, ownerA(), "kb-c", UpdateInput{
		EmbeddingModel: stringPointer("embed2"), EmbeddingDimension: intPointer(32),
	}); err == nil || !strings.Contains(err.Error(), "does not match model") {
		t.Fatalf("dimension mismatch = %v", err)
	}

	if err := db.Create(&models.Document{
		ID: "doc-1", TenantID: "tenant-a", KnowledgeBaseID: "kb-a", OwnerID: "owner-a",
		Filename: "a.txt", MimeType: "text/plain", Status: "STORED", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if _, err := service.Update(ctx, ownerA(), "kb-a", UpdateInput{EmbeddingModel: stringPointer("embed2")}); err == nil || !strings.Contains(err.Error(), "cannot be changed") {
		t.Fatalf("binding change with documents = %v", err)
	}
}

func TestDeleteKnowledgeBase(t *testing.T) {
	service, db := newKBFixture(t)
	ctx := context.Background()
	if err := service.Delete(ctx, viewerA(), "kb-a"); !errors.Is(err, apperror.ErrForbidden) {
		t.Fatalf("viewer Delete = %v, want forbidden", err)
	}
	if err := service.Delete(ctx, ownerA(), "kb-a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var knowledgeBases, members int64
	if err := db.Model(&models.KnowledgeBase{}).Where("id = ?", "kb-a").Count(&knowledgeBases).Error; err != nil {
		t.Fatalf("count knowledge bases: %v", err)
	}
	if err := db.Model(&models.KnowledgeBaseMember{}).Where("knowledge_base_id = ?", "kb-a").Count(&members).Error; err != nil {
		t.Fatalf("count members: %v", err)
	}
	if knowledgeBases != 0 || members != 0 {
		t.Fatalf("delete left rows: knowledge_bases=%d members=%d", knowledgeBases, members)
	}
}

func TestKnowledgeBaseMembers(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		service, _ := newKBFixture(t)
		ctx := context.Background()
		members, err := service.ListMembers(ctx, ownerA(), "kb-a")
		if err != nil || len(members) != 1 || members[0].UserID != "viewer-a" {
			t.Fatalf("ListMembers = %+v, %v", members, err)
		}
		if _, err := service.ListMembers(ctx, viewerA(), "kb-a"); !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("viewer ListMembers = %v, want forbidden", err)
		}
	})

	t.Run("add validation", func(t *testing.T) {
		service, _ := newKBFixture(t)
		ctx := context.Background()
		if _, err := service.AddMember(ctx, viewerA(), "kb-a", AddMemberInput{UserID: "member-c", Role: "viewer"}); !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("viewer AddMember = %v, want forbidden", err)
		}
		if _, err := service.AddMember(ctx, ownerA(), "kb-a", AddMemberInput{UserID: "member-c", Role: "root"}); err == nil {
			t.Fatal("invalid role must be rejected")
		}
		if _, err := service.AddMember(ctx, ownerA(), "kb-a", AddMemberInput{UserID: "ghost", Role: "viewer"}); !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("unknown tenant member = %v, want not found", err)
		}
	})

	t.Run("add and remove", func(t *testing.T) {
		service, db := newKBFixture(t)
		ctx := context.Background()
		member, err := service.AddMember(ctx, ownerA(), "kb-a", AddMemberInput{UserID: "member-c", Role: "viewer"})
		if err != nil {
			t.Fatalf("AddMember: %v", err)
		}
		if member.UserID != "member-c" || member.Role != "viewer" {
			t.Fatalf("member = %+v", member)
		}
		var knowledgeBase models.KnowledgeBase
		if err := db.First(&knowledgeBase, "id = ?", "kb-a").Error; err != nil {
			t.Fatalf("load knowledge base: %v", err)
		}
		if knowledgeBase.ACLVersion != 2 {
			t.Fatalf("acl version = %d, want 2", knowledgeBase.ACLVersion)
		}
		if err := service.RemoveMember(ctx, ownerA(), "kb-a", "owner-a"); err == nil || !strings.Contains(err.Error(), "owner cannot be removed") {
			t.Fatalf("remove owner = %v", err)
		}
		if err := service.RemoveMember(ctx, ownerA(), "kb-a", "ghost"); !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("remove unknown = %v, want not found", err)
		}
		if err := service.RemoveMember(ctx, ownerA(), "kb-a", "member-c"); err != nil {
			t.Fatalf("RemoveMember: %v", err)
		}
		if err := db.First(&knowledgeBase, "id = ?", "kb-a").Error; err != nil {
			t.Fatalf("reload knowledge base: %v", err)
		}
		if knowledgeBase.ACLVersion != 3 {
			t.Fatalf("acl version = %d, want 3", knowledgeBase.ACLVersion)
		}
		if err := service.RemoveMember(ctx, viewerA(), "kb-a", "member-c"); !errors.Is(err, apperror.ErrForbidden) {
			t.Fatalf("viewer RemoveMember = %v, want forbidden", err)
		}
	})

	t.Run("projection invalidation failure", func(t *testing.T) {
		service, _ := newKBFixture(t)
		service.SetProjectionLifecycle(fakeProjectionLifecycle{invalidateErr: errors.New("projection down")})
		_, err := service.AddMember(context.Background(), ownerA(), "kb-a", AddMemberInput{UserID: "member-c", Role: "viewer"})
		if err == nil || !strings.Contains(err.Error(), "invalidate knowledge projection") {
			t.Fatalf("AddMember projection failure = %v", err)
		}
	})
}
