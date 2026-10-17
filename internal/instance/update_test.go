package instance

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
)

func runSelection(t *testing.T, s *Service, next string, rollback bool) error {
	t.Helper()
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO users(id,username,role,created_at,updated_at) VALUES(1,'admin','admin','now','now') ON CONFLICT DO NOTHING"); err != nil {
		t.Fatal(err)
	}
	chosen := domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: next}
	requirements, err := managedruntime.Select(chosen)
	if err != nil {
		t.Fatal(err)
	}
	return s.withStoppedSession(t.Context(), "alpha", func(session *StoppedSession) error {
		return session.WithCoordinatorScope(t.Context(), func(ctx context.Context, scope *TransactionScope) error {
			detail, err := s.Get(ctx, "alpha")
			if err != nil {
				return err
			}
			return scope.WithSelectionParticipant(ctx, detail.Instance.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), chosen, domain.JavaRuntime{ID: "verified-runtime", Major: requirements.JavaMajor}, admin, func(p *SelectionParticipant) error {
				return scope.WithCommit(ctx, func(ctx context.Context, tx *sql.Tx) error {
					if err := p.Commit(ctx, tx); err != nil {
						return err
					}
					if rollback {
						return errors.New("injected pointer failure")
					}
					return nil
				})
			})
		})
	})
}
func TestSelectionParticipantTransitionsPrivateManagementAtomically(t *testing.T) {
	s, _, _ := setup(t)
	d := draft("alpha", 25565)
	d.Instance.MinecraftVersion = "1.21.1"
	if _, err := s.Create(t.Context(), admin, d); err != nil {
		t.Fatal(err)
	}
	if err := runSelection(t, s, "1.21.9", false); err != nil {
		t.Fatal(err)
	}
	original, err := s.Management(t.Context(), "alpha")
	if err != nil || original.Protocol != "1.0.0" {
		t.Fatalf("legacy to MSMP: %v %v", original, err)
	}
	if err = runSelection(t, s, "26.3", false); err != nil {
		t.Fatal(err)
	}
	current, err := s.Management(t.Context(), "alpha")
	if err != nil || current.Protocol != "3.1.0" || original.Port != current.Port || original.Secret != current.Secret {
		t.Fatalf("MSMP upgrade lost private identity: %v %v", current, err)
	}
	if err = runSelection(t, s, "1.12.2", false); err != nil {
		t.Fatal(err)
	}
	var ports, management int
	if err = s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM instance_management WHERE instance_id='alpha'").Scan(&management); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM instance_tcp_ports WHERE instance_id='alpha' AND kind='management'").Scan(&ports); err != nil {
		t.Fatal(err)
	}
	detail, err := s.Get(t.Context(), "alpha")
	if err != nil || !detail.Settings.LegacyNumericEnums || detail.Settings.SimulationDistance.Available || ports != 0 || management != 0 {
		t.Fatalf("downgrade retained control/settings: %+v ports=%d management=%d err=%v", detail.Settings, ports, management, err)
	}
}
func TestSelectionParticipantRollbackPreservesCurrentPrivateConfiguration(t *testing.T) {
	s, _, _ := setup(t)
	d := draft("alpha", 25565)
	d.Instance.MinecraftVersion = "1.21.9"
	if _, err := s.Create(t.Context(), admin, d); err != nil {
		t.Fatal(err)
	}
	if err := s.withStoppedSession(t.Context(), "alpha", func(session *StoppedSession) error { _, err := session.EnsureManagement(t.Context()); return err }); err != nil {
		t.Fatal(err)
	}
	original, err := s.Management(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err = runSelection(t, s, "1.12.2", true); err == nil {
		t.Fatal("injected transaction failure lost")
	}
	current, err := s.Management(t.Context(), "alpha")
	if err != nil || current != original {
		t.Fatal("failed transaction changed credentials", err)
	}
	detail, err := s.Get(t.Context(), "alpha")
	if err != nil || detail.Instance.MinecraftVersion != "1.21.9" {
		t.Fatal("failed transaction changed selection", err)
	}
}

func TestSelectionParticipantPortRaceRollsBackSelection(t *testing.T) {
	s, _, _ := setup(t)
	d := draft("alpha", 25565)
	d.Instance.MinecraftVersion = "1.21.1"
	if _, err := s.Create(t.Context(), admin, d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(t.Context(), admin, draft("bravo", 25566)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO users(id,username,role,created_at,updated_at) VALUES(1,'admin','admin','now','now')"); err != nil {
		t.Fatal(err)
	}
	err := s.withStoppedSession(t.Context(), "alpha", func(session *StoppedSession) error {
		detail, e := session.Detail(t.Context())
		if e != nil {
			return e
		}
		return session.WithCoordinatorScope(t.Context(), func(ctx context.Context, scope *TransactionScope) error {
			return scope.WithSelectionParticipant(ctx, detail.Instance.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.9"}, domain.JavaRuntime{ID: "verified-runtime", Major: 21}, admin, func(p *SelectionParticipant) error {
				if _, e := s.db.ExecContext(ctx, "INSERT INTO instance_tcp_ports(port,instance_id,kind) VALUES(?,'bravo','management')", p.management.Port); e != nil {
					return e
				}
				return scope.WithCommit(ctx, func(ctx context.Context, tx *sql.Tx) error { return p.Commit(ctx, tx) })
			})
		})
	})
	if domain.AsError(err).Code != domain.CodePortInUse {
		t.Fatal("reservation race accepted", err)
	}
	detail, err := s.Get(t.Context(), "alpha")
	if err != nil || detail.Instance.MinecraftVersion != "1.21.1" {
		t.Fatal("reservation failure changed selection", err)
	}
}

func TestSelectionParticipantRevocationAndExpiredCopies(t *testing.T) {
	s, _, _ := setup(t)
	if _, err := s.Create(t.Context(), admin, draft("alpha", 25565)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO users(id,username,role,created_at,updated_at) VALUES(1,'admin','admin','now','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO users(id,username,role,created_at,updated_at) VALUES(2,'other','admin','now','now')"); err != nil {
		t.Fatal(err)
	}
	var retained *SelectionParticipant
	err := s.withStoppedSession(t.Context(), "alpha", func(session *StoppedSession) error {
		detail, e := session.Detail(t.Context())
		if e != nil {
			return e
		}
		return session.WithCoordinatorScope(t.Context(), func(ctx context.Context, scope *TransactionScope) error {
			return scope.WithSelectionParticipant(ctx, detail.Instance.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.11"}, domain.JavaRuntime{ID: "verified-runtime", Major: 21}, admin, func(p *SelectionParticipant) error {
				retained = p
				copy := new(SelectionParticipant)
				reflect.ValueOf(copy).Elem().Set(reflect.ValueOf(p).Elem())
				if e := copy.Commit(ctx, new(sql.Tx)); domain.AsError(e).Code != domain.CodeForbidden {
					return errors.New("copied selection capability admitted")
				}
				if _, e := s.db.ExecContext(ctx, "UPDATE users SET role='viewer' WHERE id=1"); e != nil {
					return e
				}
				return scope.WithCommit(ctx, func(ctx context.Context, tx *sql.Tx) error { return p.Commit(ctx, tx) })
			})
		})
	})
	if domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("revoked actor committed", err)
	}
	if e := retained.Commit(t.Context(), new(sql.Tx)); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal("expired participant admitted", e)
	}
	detail, err := s.Get(t.Context(), "alpha")
	if err != nil || detail.Instance.MinecraftVersion != "1.21.9" {
		t.Fatal("revoked actor changed selection", err)
	}
}
