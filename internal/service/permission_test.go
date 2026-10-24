package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/ShukeBta/MediaStationGo/internal/model"
	"github.com/ShukeBta/MediaStationGo/internal/repository"
)

func TestPermissionSavePersistsDisabledMenuValues(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.UserPermission{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	user := &model.User{Username: "viewer", PasswordHash: "hash", Role: "user", IsActive: true}
	if err := repos.User.Create(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	permissions := NewPermissionService(zap.NewNop(), repos)
	initial := DefaultPermissions(user.ID)
	initial.CanViewDiscover = true
	initial.CanBlacklist = true
	if err := permissions.Save(t.Context(), user.ID, initial); err != nil {
		t.Fatal(err)
	}
	initial.CanViewDashboard = false
	initial.CanPlayMedia = false
	initial.CanBlacklist = false
	if err := permissions.Save(t.Context(), user.ID, initial); err != nil {
		t.Fatal(err)
	}
	got, err := permissions.Effective(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.CanViewDashboard || got.CanPlayMedia || got.CanBlacklist || !got.CanViewDiscover {
		t.Fatalf("permissions = %#v", got)
	}
}

func TestBlacklistPermissionDefaultsAndReset(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.AutoMigrate(&model.User{}, &model.UserPermission{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	permissions := NewPermissionService(zap.NewNop(), repos)
	for _, tier := range []string{"free", "plus"} {
		user := &model.User{Username: tier, Role: "user", Tier: tier, IsActive: true}
		if err := repos.User.Create(t.Context(), user); err != nil {
			t.Fatal(err)
		}
		row, err := permissions.Effective(t.Context(), user.ID)
		if err != nil || row == nil || row.CanBlacklist || !row.CanFavorite || row.PermissionMap()["can_blacklist"] {
			t.Fatalf("%s default permission=%+v err=%v", tier, row, err)
		}
		row.CanBlacklist = true
		row.CanFavorite = false
		if err := permissions.Save(t.Context(), user.ID, row); err != nil {
			t.Fatal(err)
		}
		row, err = permissions.Effective(t.Context(), user.ID)
		if err != nil || row == nil || !row.CanBlacklist || row.CanFavorite || !row.PermissionMap()["can_blacklist"] {
			t.Fatalf("%s independent grant=%+v err=%v", tier, row, err)
		}
		row, err = permissions.Reset(t.Context(), user.ID)
		if err != nil || row == nil || row.CanBlacklist || !row.CanFavorite {
			t.Fatalf("%s reset=%+v err=%v", tier, row, err)
		}
		row, err = permissions.Effective(t.Context(), user.ID)
		if err != nil || row == nil || row.CanBlacklist {
			t.Fatalf("%s persisted reset=%+v err=%v", tier, row, err)
		}
	}
	admin := &model.User{Username: "admin", Role: "admin", IsActive: true}
	if err := repos.User.Create(t.Context(), admin); err != nil {
		t.Fatal(err)
	}
	row, err := permissions.Effective(t.Context(), admin.ID)
	if err != nil || row == nil || !row.CanBlacklist {
		t.Fatalf("admin grant=%+v err=%v", row, err)
	}
	if model.NewDefaultPermission("new-user").CanBlacklist {
		t.Fatal("model defaults must not grant blacklist access")
	}
}
