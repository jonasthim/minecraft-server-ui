package instance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

func TestManagementPrivateReservationMaterializationAndRetry(t *testing.T) {
	s, p, native := setup(t)
	ctx := t.Context()
	d := draft("managed", 25565)
	d.Instance.MinecraftVersion = "1.21.9"
	if _, err := s.Create(ctx, admin, d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Management(ctx, "managed"); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatalf("unprovisioned: %v", err)
	}
	q := jobs.New(s.db, nil)
	s = New(s.db, p, queueIdleGuard{q, native}, WithJobMutationGuard(&composedJobGuard{native: native}))
	var first ManagementConfig
	fail := true
	if err := q.Register("management", func(ctx context.Context, e *jobs.Execution) error {
		return s.WithStoppedJob(ctx, e, func(session *StoppedSession) error {
			cfg, err := session.EnsureManagement(ctx)
			if err != nil {
				return err
			}
			first = cfg
			if fail {
				dirs, _ := p.For("managed")
				_ = os.Remove(filepath.Join(dirs.Server, "server.properties"))
				_ = os.Mkdir(filepath.Join(dirs.Server, "server.properties"), 0700)
				fail = false
				_, err = session.EnsureManagement(ctx)
				if err == nil {
					return domain.E(domain.CodeInternal, "materialization failure hidden")
				}
				_ = os.Remove(filepath.Join(dirs.Server, "server.properties"))
				return session.Materialize(ctx)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop(context.Background())
	job, err := q.Enqueue(ctx, jobs.Request{InstanceID: "managed", Kind: "management", RequestedBy: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitInstanceJob(t, q, job.ID)
	j, _ := q.Get(ctx, job.ID)
	if j.State != domain.JobSucceeded {
		t.Fatalf("job: %+v", j)
	}
	cfg, err := s.Management(ctx, "managed")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port == 25565 || cfg.Host != "127.0.0.1" || len(cfg.Secret) != 40 || cfg.Secret != first.Secret {
		t.Fatalf("invalid/idempotency config port %d", cfg.Port)
	}
	raw, _ := json.Marshal(cfg)
	if string(raw) != "{}" {
		t.Fatalf("private JSON leaked: %s", raw)
	}
	dirs, _ := p.For("managed")
	body, _ := os.ReadFile(filepath.Join(dirs.Server, "server.properties"))
	if !strings.Contains(string(body), "management-server-secret="+cfg.Secret) || !strings.Contains(string(body), "management-server-host=127.0.0.1") {
		t.Fatal("private configuration not retained")
	}
	if _, err = s.Create(ctx, admin, draft("collision", cfg.Port)); domain.AsError(err).Code != domain.CodePortInUse {
		t.Fatalf("cross-kind create: %v", err)
	}
	_, err = s.Create(ctx, admin, draft("other", 25566))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := s.Get(ctx, "other")
	other.Instance.GamePort = cfg.Port
	if _, err = s.Update(ctx, admin, "other", Draft{Instance: other.Instance, Config: other.Config}); domain.AsError(err).Code != domain.CodePortInUse {
		t.Fatalf("cross-kind edit: %v", err)
	}
	// SQL triggers enforce the same invariant even for independent writers.
	if _, err = s.db.ExecContext(ctx, `UPDATE instances SET game_port=? WHERE id='other'`, cfg.Port); err == nil {
		t.Fatal("DB allowed cross-kind collision")
	}
	if err = s.Delete(ctx, admin, "managed", "managed"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM instance_tcp_ports WHERE instance_id='managed'`).Scan(&n)
	if n != 0 {
		t.Fatal("reservation leaked after delete")
	}
	if _, err = s.Create(ctx, admin, draft("reused", cfg.Port)); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCanonicalPortReservation(t *testing.T) {
	s, _, _ := setup(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"first", "second"} {
		go func(id string) { <-start; _, err := s.Create(t.Context(), admin, draft(id, 25565)); results <- err }(id)
	}
	close(start)
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else if domain.AsError(err).Code == domain.CodePortInUse {
			conflict++
		} else {
			t.Fatalf("unexpected contention result %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM instance_tcp_ports WHERE port=25565`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("canonical reservations=%d %v", count, err)
	}
}

func TestManagementProtocolVersionBoundaries(t *testing.T) {
	for version, expected := range map[string]string{"1.12.2": "", "1.21.8": "", "1.21.9": "1.0.0", "1.21.10": "1.0.0", "1.21.11": "2.0.0", "26.1": "2.0.0", "26.2": "3.0.0", "26.3": "3.1.0", "26.3-snapshot": ""} {
		t.Run(version, func(t *testing.T) {
			actual, err := ManagementProtocol(version)
			if actual != expected || (expected == "") != (err != nil) {
				t.Fatalf("protocol %q error %v", actual, err)
			}
		})
	}
}
