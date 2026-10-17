package selfupdate

import (
	"bytes"
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"net/http"
	"runtime"
	"testing"
	"time"
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

func TestStageStatusAndRetryRecoverPublishedReceiptWithoutNetworkOrActivation(t *testing.T) {
	ctx := context.Background()
	archive := archiveFixture(t, runtime.GOOS, []string{binaryName(runtime.GOOS), "LICENSE", "THIRD_PARTY_NOTICES.txt"})
	trust, b, sig := stageFixture(t, runtime.GOOS, archive)
	dir := t.TempDir()
	stager := Stager{Dir: dir, Trust: trust, CurrentVersion: "v1.2.2", Schema: 16, OS: runtime.GOOS, Arch: runtime.GOARCH, Now: func() time.Time { return fixtureNow }}
	stage, err := stager.Stage(ctx, "v1.2.3", b, sig, bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	database, err := db.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	actor, err := db.NewUsers(database).Create(ctx, db.NewUser{Username: "admin", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s := New(Config{Trust: trust, CurrentVersion: "v1.2.2", Schema: 16, StageDir: dir, DB: database})
		s.now = func() time.Time { return fixtureNow }
		s.source.client.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
			t.Fatal("retry downloaded already verified release")
			return nil, nil
		})
		status := s.Status(ctx)
		if len(status.Stages) != 1 || status.Stages[0] != stage || status.StagingError != "" {
			t.Fatal("reload lost authoritative stage", status)
		}
		again, err := s.Stage(ctx, "v1.2.3", *actor)
		if err != nil || again != stage {
			t.Fatal("lost response retry failed", again, err)
		}
		// Neither status discovery nor retry can activate: no native broker exists.
		if status.ActivationAvailable {
			t.Fatal("stage discovery activated native updater")
		}
	}
}
