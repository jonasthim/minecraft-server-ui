package selfupdate

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
)

func TestUnconfiguredServiceCannotActivate(t *testing.T) {
	s := New(Config{CurrentVersion: "0.0.0-dev", Schema: 16})
	status := s.Status(context.Background())
	if status.TrustConfigured || status.ActivationAvailable || status.DisabledReason == "" {
		t.Fatal(status)
	}
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
		_, err := s.Check(context.Background(), "v1.2.3", domain.User{Role: role})
		if err == nil {
			t.Fatal("check enabled", role)
		}
		if err := s.Apply(context.Background(), ApplyInput{StageID: digest([]byte("stage")), Confirm: true}, domain.User{Role: role}); err == nil {
			t.Fatal("activation enabled", role)
		}
	}
}
func TestFreshAdministratorRoleIsReadFromDB(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	actor, err := db.NewUsers(database).Create(ctx, db.NewUser{Username: "admin", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.NewUsers(database).Create(ctx, db.NewUser{Username: "other-admin", Role: domain.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{DB: database})
	if _, err := s.freshAdmin(ctx, *actor); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE users SET role='viewer' WHERE id=?`, actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.freshAdmin(ctx, *actor); err == nil {
		t.Fatal("stale administrator role accepted")
	}
	if _, err := database.Exec(`UPDATE users SET role='admin',disabled=1 WHERE id=?`, actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.freshAdmin(ctx, *actor); err == nil {
		t.Fatal("disabled administrator accepted")
	}
}
