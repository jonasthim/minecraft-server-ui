package players

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSavedListPreservesUnrelatedBytesAndRejectsCorruption(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "whitelist.json")
	original := "[\n {\"uuid\":\"00000000-0000-0000-0000-000000000001\", \"name\":\"Alex\", \"custom\": 42},\n {\"uuid\":\"00000000-0000-0000-0000-000000000002\",\"name\":\"Steve\"}\n]\n"
	os.WriteFile(file, []byte(original), 0600)
	r, e := os.OpenRoot(root)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	m := console.PlayerMutation{Action: "whitelist_add", UUID: "00000000-0000-0000-0000-000000000003", Name: "Sam"}
	if e = mutateSaved(t.Context(), r, m, time.Now()); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), `{"uuid":"00000000-0000-0000-0000-000000000001", "name":"Alex", "custom": 42}`) {
		t.Fatal("unrelated formatting lost", string(b))
	}
	os.WriteFile(file, []byte("{broken"), 0600)
	if e = mutateSaved(t.Context(), r, m, time.Now()); e == nil {
		t.Fatal("corruption replaced")
	}
	b, _ = os.ReadFile(file)
	if string(b) != "{broken" {
		t.Fatal("corruption discarded")
	}
	os.Remove(file)
	os.Symlink(filepath.Join(root, "outside"), file)
	if e = mutateSaved(t.Context(), r, m, time.Now()); e == nil {
		t.Fatal("symlink accepted")
	}
}
func TestSavedDuplicateIdentityAndRenameConflicts(t *testing.T) {
	for _, body := range []string{`[{"uuid":"00000000-0000-0000-0000-000000000001","name":"Alex"},{"uuid":"00000000-0000-0000-0000-000000000001","name":"Sam"}]`, `[{"uuid":"00000000-0000-0000-0000-000000000001","name":"Alex"},{"uuid":"00000000-0000-0000-0000-000000000002","name":"alex"}]`, `[null]`, `[{"uuid":"bad","name":"Alex"}]`} {
		if _, e := parseSaved([]byte(body)); e == nil {
			t.Fatal("accepted ambiguous/corrupt", body)
		}
	}
}
func fixture(t *testing.T) *Service {
	d, e := db.OpenMemory(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { d.Close() })
	p, e := instance.NewPaths(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	_, e = d.ExecContext(t.Context(), `INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port,created_at,updated_at) VALUES('test','Test','vanilla','1.16.5',8,1024,25565,'2026-10-16','2026-10-16')`)
	if e != nil {
		t.Fatal(e)
	}
	return New(d, p, nil, nil, nil, nil)
}
func TestObservedHistoryDedupeGenerationAndRetention(t *testing.T) {
	s := fixture(t)
	now := time.Now().UTC()
	snap := console.PlayerSnapshot{InstanceID: "test", Generation: "one", Known: true, Complete: true, ObservedAt: now, Players: []console.PlayerIdentity{{Name: "Alex", Authority: "name_only", Source: "fresh_native_console_list"}}}
	for i := 0; i < 2; i++ {
		if e := s.record(t.Context(), snap); e != nil {
			t.Fatal(e)
		}
	}
	page, e := s.History(t.Context(), "test", 0, 100)
	if e != nil || len(page.Sessions) != 1 {
		t.Fatalf("dedupe %+v %v", page, e)
	}
	snap.Generation = "two"
	snap.ObservedAt = now.Add(time.Second)
	if e = s.record(t.Context(), snap); e != nil {
		t.Fatal(e)
	}
	page, e = s.History(t.Context(), "test", 0, 100)
	if e != nil || len(page.Sessions) != 2 || !page.Sessions[0].Gap {
		t.Fatalf("restart provenance %+v %v", page, e)
	}
	snap.ObservedAt = now.Add(31 * 24 * time.Hour)
	if e = s.record(t.Context(), snap); e != nil {
		t.Fatal(e)
	}
	page, e = s.History(t.Context(), "test", 0, 100)
	if e != nil || len(page.Sessions) != 1 || !page.Gap {
		t.Fatalf("retention %+v %v", page, e)
	}
}
func TestMutationAuthorizationAndValidation(t *testing.T) {
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
		for _, action := range []string{"kick", "whitelist_add", "ban", "op", "deop"} {
			e := authorize(domain.User{ID: 1, Role: role}, Mutation{Mode: "online", PlayerMutation: console.PlayerMutation{Action: action, Name: "Alex"}})
			want := role.AtLeast(domain.RoleOperator) && ((action != "op" && action != "deop") || role == domain.RoleAdmin)
			if (e == nil) != want {
				t.Fatalf("%s %s: %v", role, action, e)
			}
		}
	}
	if e := authorize(domain.User{ID: 1, Role: domain.RoleAdmin}, Mutation{Mode: "offline", PlayerMutation: console.PlayerMutation{Action: "kick", Name: "Alex"}}); e == nil {
		t.Fatal("offline kick")
	}
	_ = context.Background()
}

type playerStoppedGuard struct {
	running bool
	calls   int
}

func (g *playerStoppedGuard) WithStopped(ctx context.Context, id string, fn func() error) error {
	if g.running {
		return domain.E(domain.CodeConflict, "native server is running")
	}
	return fn()
}
func (g *playerStoppedGuard) WithStoppedForJob(ctx context.Context, e *jobs.Execution, fn func(string) error) error {
	return e.WithInstanceMutation(ctx, func(id string) error { g.calls++; return g.WithStopped(ctx, id, func() error { return fn(id) }) })
}
func TestQueuedOfflineMutationAuthorityAndSingleStoppedAdmission(t *testing.T) {
	for _, scenario := range []string{"success", "revoked", "running", "corrupt"} {
		t.Run(scenario, func(t *testing.T) {
			d, e := db.OpenMemory(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			defer d.Close()
			p, e := instance.NewPaths(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			_, e = d.ExecContext(t.Context(), `INSERT INTO users(id,username,role,created_at,updated_at) VALUES(1,'admin','admin','2026-10-16','2026-10-16')`)
			if e != nil {
				t.Fatal(e)
			}
			_, e = d.ExecContext(t.Context(), `INSERT INTO users(id,username,role,created_at,updated_at) VALUES(2,'secondadmin','admin','2026-10-16','2026-10-16')`)
			if e != nil {
				t.Fatal(e)
			}
			q := jobs.New(d, nil)
			guard := &playerStoppedGuard{}
			instances := instance.New(d, p, guard, instance.WithJobMutationGuard(guard))
			actor := domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}
			_, e = instances.Create(t.Context(), actor, instance.Draft{Instance: domain.Instance{ID: "test", Name: "Test", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
			if e != nil {
				t.Fatal(e)
			}
			s := New(d, p, instances, nil, q, nil)
			if e = s.Register(); e != nil {
				t.Fatal(e)
			}
			if e = q.StartPaused(t.Context()); e != nil {
				t.Fatal(e)
			}
			defer q.Stop(context.Background())
			dirs, _ := p.For("test")
			file := filepath.Join(dirs.Server, "whitelist.json")
			if scenario == "corrupt" {
				os.WriteFile(file, []byte("{broken"), 0600)
			}
			m := Mutation{Mode: "offline", PlayerMutation: console.PlayerMutation{Action: "whitelist_add", UUID: "00000000-0000-0000-0000-000000000001", Name: "Alex"}}
			job, e := s.Enqueue(t.Context(), "test", m, actor)
			if e != nil {
				t.Fatal(e)
			}
			if scenario == "revoked" {
				if _, e = d.ExecContext(t.Context(), `UPDATE users SET role='viewer' WHERE id=1`); e != nil {
					t.Fatal(e)
				}
			}
			guard.running = scenario == "running"
			if e = q.Resume(); e != nil {
				t.Fatal(e)
			}
			var finished jobs.Job
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				finished, e = q.Get(t.Context(), job.ID)
				if e != nil {
					t.Fatal(e)
				}
				if finished.State != domain.JobQueued && finished.State != domain.JobRunning {
					break
				}
				time.Sleep(time.Millisecond)
			}
			b, _ := os.ReadFile(file)
			if scenario == "success" {
				if finished.State != domain.JobSucceeded || !strings.Contains(string(b), "Alex") || guard.calls != 1 {
					t.Fatalf("single stopped operation %+v %s calls%d", finished, b, guard.calls)
				}
			} else {
				if finished.State != domain.JobFailed {
					t.Fatalf("unsafe success %+v", finished)
				}
				if scenario == "corrupt" && string(b) != "{broken" {
					t.Fatal("corrupt file replaced")
				}
				if scenario == "revoked" && guard.calls != 0 {
					t.Fatal("revoked actor reached stopped admission")
				}
			}
			if scenario != "success" {
				var logs int
				if e = d.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM job_logs WHERE job_id=?`, job.ID).Scan(&logs); e != nil || logs == 0 {
					t.Fatal("failed or partial operation has no safe job output", e)
				}
			}
			var n int
			if e = d.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE action='players.result'`).Scan(&n); e != nil || n != 1 {
				t.Fatal("result audit missing", n, e)
			}
		})
	}
}
func TestPlayerLogCursorDedupeAndIndependentPagination(t *testing.T) {
	s := fixture(t)
	s.console = console.New(s.db, s.paths, nil, nil, nil)
	snapshot := console.PlayerSnapshot{InstanceID: "test", ObservedAt: time.Now().UTC(), Players: []console.PlayerIdentity{}, Gap: true}
	if err := s.record(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"[12:00:00] [Server thread/INFO]: Alex joined the game", "[12:00:01] [Server thread/INFO]: <Alex> Steve joined the game", "[12:00:02] [Server thread/INFO]: Alex left the game"} {
		_, err := s.db.ExecContext(t.Context(), `INSERT INTO console_lines(instance_id,created_at,message) VALUES('test',?,?)`, time.Now().UTC().Format(time.RFC3339Nano), message)
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := s.consumeLogs(t.Context(), "test"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.HistoryWithEvents(t.Context(), "test", 0, 0, 1)
	if err != nil || len(page.Events) != 1 || !page.EventGap || page.Events[0].Kind != "join_observed" || page.NextCursor != 0 || page.NextEventCursor == 0 {
		t.Fatalf("first independent page %+v %v", page, err)
	}
	page, err = s.HistoryWithEvents(t.Context(), "test", 0, page.NextEventCursor, 1)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != "leave_observed" || page.Events[0].Player.UUID != "" {
		t.Fatalf("second independent page %+v %v", page, err)
	}
	page, err = s.HistoryWithEvents(t.Context(), "test", 0, page.NextEventCursor, 1)
	if err != nil || len(page.Events) != 0 {
		t.Fatalf("dedupe %+v %v", page, err)
	}
}

func TestPlayerHistoryRetentionCountAndStaleness(t *testing.T) {
	s := fixture(t)
	now := time.Now().UTC()
	snap := console.PlayerSnapshot{InstanceID: "test", ObservedAt: now.Add(-time.Minute), Generation: "one", Known: true, Complete: true, Players: []console.PlayerIdentity{}}
	if err := s.record(t.Context(), snap); err != nil {
		t.Fatal(err)
	}
	cached, err := s.Snapshot(t.Context(), "test")
	if err != nil || cached.Known || !cached.Gap {
		t.Fatalf("stale presence %+v %v", cached, err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10001; i++ {
		_, err = tx.ExecContext(t.Context(), `INSERT INTO player_sessions(instance_id,identity_key,identity_json,generation,first_seen,last_seen,ended_at) VALUES('test',?,'{"name":"Alex","authority":"name_only","source":"test"}','one',?,?,?)`, fmt.Sprint(i), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	snap.ObservedAt = now
	if err = s.record(t.Context(), snap); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM player_sessions`).Scan(&count); err != nil || count != 10000 {
		t.Fatal("unbounded session history", count, err)
	}
	page, err := s.History(t.Context(), "test", 0, 1)
	if err != nil || !page.Gap || len(page.Sessions) != 1 || page.Sessions[0].ID != 2 {
		t.Fatalf("retention cursor %+v %v", page, err)
	}
}
func TestPlayerJSONRejectsDuplicateKeys(t *testing.T) {
	if _, err := parseSaved([]byte(`[{"uuid":"00000000-0000-0000-0000-000000000001","name":"Alex","name":"Steve"}]`)); err == nil {
		t.Fatal("ambiguous duplicate JSON key accepted")
	}
}
func TestMalformedOperatorFieldsNeverActivateOtherChanges(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "ops.json")
	original := `[{"uuid":"00000000-0000-0000-0000-000000000001","name":"Alex","level":"bad","bypassesPlayerLimit":false}]`
	os.WriteFile(file, []byte(original), 0600)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	err = mutateSaved(t.Context(), root, console.PlayerMutation{Action: "op", UUID: "00000000-0000-0000-0000-000000000002", Name: "Steve"}, time.Now())
	b, _ := os.ReadFile(file)
	if err == nil || string(b) != original {
		t.Fatalf("malformed ops file changed: %s %v", b, err)
	}
}
func TestHistoryRejectsForeignInstanceCursorsWithGap(t *testing.T) {
	s := fixture(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port,created_at,updated_at) VALUES('other','Other','vanilla','1.16.5',8,1024,25566,'2026-10-16','2026-10-16')`)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"test", "other", "test"} {
		_, err = s.db.ExecContext(t.Context(), `INSERT INTO player_sessions(instance_id,identity_key,identity_json,generation,first_seen,last_seen,ended_at) VALUES(?,'key','{"name":"Alex","authority":"name_only","source":"fixture"}','one',?,?,?)`, id, now, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.History(t.Context(), "test", 2, 10)
	if err != nil || !page.Gap || len(page.Sessions) != 2 {
		t.Fatalf("foreign cursor silently skipped rows %+v %v", page, err)
	}
}

func TestNullOperatorFieldsRejectReadsAndDoNotStageMutation(t *testing.T) {
	for _, field := range []string{"level", "bypassesPlayerLimit"} {
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			fields := map[string]any{"uuid": "00000000-0000-0000-0000-000000000001", "name": "Alex", "level": 4, "bypassesPlayerLimit": false}
			fields[field] = nil
			entry, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			original := append(append([]byte("["), entry...), ']')
			file := filepath.Join(dir, "ops.json")
			if err = os.WriteFile(file, original, 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if _, err = savedList(t.Context(), root, "operators"); err == nil {
				t.Error("null operator permission field accepted by saved read")
			}
			err = mutateSaved(t.Context(), root, console.PlayerMutation{Action: "op", UUID: "00000000-0000-0000-0000-000000000002", Name: "Steve"}, time.Now())
			if err == nil {
				t.Error("null operator permission field survived another OP mutation")
			}
			current, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, current) {
				t.Errorf("malformed ops file changed: %s", current)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("null field rejection left staging artifacts: %+v", entries)
			}
		})
	}
}
