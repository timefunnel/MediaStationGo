package service

import (
	"errors"
	"testing"

	"go.uber.org/zap"

	"github.com/ShukeBta/MediaStationGo/internal/config"
	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func TestMediaVersionOwnerCanDeleteOnlyOwnedVersion(t *testing.T) {
	db := newServiceTestDB(t, &model.User{}, &model.Media{}, &model.ResourceImportJob{})
	repos := repository.New(db)
	user := model.User{Username: "owner", PasswordHash: "x", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	owned := model.Media{LibraryID: "library-1", Title: "Sintel", TMDbID: 123, Path: "cloud://openlist/Movies/Sintel.1080p.mkv"}
	other := model.Media{LibraryID: "library-1", Title: "Sintel", TMDbID: 123, Path: "cloud://openlist/Movies/Sintel.2160p.mkv"}
	if err := db.Create(&owned).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	job := model.ResourceImportJob{
		UserID: user.ID, LibraryID: owned.LibraryID, SearchSessionID: "session",
		CandidateJSON: "{}", CandidateTitle: "Sintel", IdempotencyKey: "owned-version",
		Status: ResourceImportStatusCompleted, Stage: "completed", MediaID: owned.ID, Attempt: 1,
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}

	svc := NewMediaService(&config.Config{}, zap.NewNop(), repos)
	versions, err := svc.ListMediaVersions(t.Context(), other.ID, user.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions.Items) != 2 || !versions.CanManageVersions {
		t.Fatalf("versions = %+v", versions)
	}
	permissions := map[string]bool{}
	for _, item := range versions.Items {
		permissions[item.ID] = item.CanManage
	}
	if !permissions[owned.ID] || permissions[other.ID] {
		t.Fatalf("version permissions = %#v", permissions)
	}
	if _, err := svc.DeleteMediaVersion(t.Context(), other.ID, other.ID, user.ID, false); !errors.Is(err, ErrMediaVersionForbidden) {
		t.Fatalf("delete unowned version err = %v", err)
	}
	result, err := svc.DeleteMediaVersion(t.Context(), other.ID, owned.ID, user.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedID != owned.ID || result.NextMediaID != other.ID {
		t.Fatalf("delete result = %+v", result)
	}
	var deleted model.Media
	if err := db.Unscoped().First(&deleted, "id = ?", owned.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !deleted.DeletedAt.Valid || deleted.DeletionKind != "version" || deleted.DeletedByUserID != user.ID {
		t.Fatalf("deleted version = %+v", deleted)
	}
}

func TestAdminCanManageEveryMediaVersion(t *testing.T) {
	db := newServiceTestDB(t, &model.Media{}, &model.ResourceImportJob{})
	repos := repository.New(db)
	first := model.Media{LibraryID: "library-1", Title: "Sintel", TMDbID: 123, Path: "cloud://openlist/Movies/Sintel.A.mkv"}
	second := model.Media{LibraryID: "library-1", Title: "Sintel", TMDbID: 123, Path: "cloud://openlist/Movies/Sintel.B.mkv"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	versions, err := NewMediaService(&config.Config{}, zap.NewNop(), repos).
		ListMediaVersions(t.Context(), first.ID, "admin", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range versions.Items {
		if !item.CanManage {
			t.Fatalf("admin cannot manage version %+v", item)
		}
	}
}

func TestListMediaVersionsDoesNotMergeDomainPrefixedTitles(t *testing.T) {
	db := newServiceTestDB(t, &model.Media{}, &model.ResourceImportJob{})
	repos := repository.New(db)
	rows := []model.Media{
		{
			LibraryID: "other-library",
			Title:     "mtcang.com v",
			Path:      "cloud://openlist/115/其他/作品二十/mtcang.com v.mp4",
		},
		{
			LibraryID: "other-library",
			Title:     "mtcang.com 跳蛋",
			Path:      "cloud://openlist/115/其他/作品十/mtcang.com 跳蛋.mp4",
		},
		{
			LibraryID: "other-library",
			Title:     "mtcang.com spa",
			Path:      "cloud://openlist/115/其他/作品十一/mtcang.com spa.mp4",
		},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	versions, err := NewMediaService(&config.Config{}, zap.NewNop(), repos).
		ListMediaVersions(t.Context(), rows[0].ID, "admin", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions.Items) != 1 || versions.Items[0].ID != rows[0].ID {
		t.Fatalf("domain-prefixed titles were merged as versions: %#v", versions.Items)
	}
}
