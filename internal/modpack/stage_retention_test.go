package modpack

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Expired temporary artifacts may be freed without deleting immutable update
// evidence. Live work and durable blockers must retain readable staged bytes.
func TestUpdateStageRetention(t *testing.T) {
	for _, tc := range []struct {
		name, owner, state, marker, jobState string
		keep                                 bool
	}{
		{name: "expired abandoned preview", owner: "preview", keep: false},
		{name: "unexpired upload to reprepare", owner: "preview", state: "future", keep: true},
		{name: "completed history", owner: "transaction", state: "complete", keep: false},
		{name: "terminal transaction still owned by queued job", owner: "transaction", state: "complete", jobState: "queued", keep: true},
		{name: "malformed expiry remains conservative", owner: "preview", state: "malformed", keep: true},
		{name: "rolled back history", owner: "transaction", state: "rolled_back", keep: false},
		{name: "failed boot recovery", owner: "transaction", state: "blocked", keep: true},
		{name: "prepared recovery", owner: "transaction", state: "prepared", keep: true},
		{name: "queued expired preview", owner: "job", state: "queued", keep: true},
		{name: "running expired preview", owner: "job", state: "running", keep: true},
		{name: "queued reprepare receipt", owner: "prepare", state: "queued", keep: true},
		{name: "terminal job with update blocker", owner: "transaction", state: "complete", marker: "update-transaction.json", keep: true},
		{name: "terminal job with world risk hold", owner: "transaction", state: "complete", marker: "update-world-risk.json", keep: true},
		{name: "orphan blocker", owner: "preview", marker: "update-transaction.json", keep: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPackFixture(t)
			s, database := f.s, f.database
			if err := f.q.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			st, err := s.stage(t.Context(), "alpha", 1, strings.NewReader("bytes"), 5)
			if err != nil {
				t.Fatal(err)
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := database.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("UPDATE modpack_stages SET created_at='2000-01-01T00:00:00Z'")
			expiry := "2000-01-01T00:00:00Z"
			if tc.state == "future" {
				expiry = "2100-01-01T00:00:00Z"
			}
			if tc.state == "malformed" {
				expiry = "unparseable"
			}
			exec("INSERT INTO update_previews(id,instance_id,actor_id,private_json,created_at,expires_at) VALUES('preview','alpha',1,?,'2000-01-01T00:00:00Z',?)", `{"receipt":"`+st.ID+`"}`, expiry)
			if tc.owner == "transaction" {
				exec("INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,journal_json,updated_at) VALUES('transaction','alpha','preview','job',1,'update',?,'protected-backup',?,'now')", tc.state, `{"receipt":"`+st.ID+`"}`)
				jobState := tc.jobState
				if jobState == "" {
					jobState = "succeeded"
				}
				exec("INSERT INTO jobs(id,instance_id,kind,state,created_at) VALUES('job','alpha','instance_update',?,'now')", jobState)
			}
			if tc.owner == "job" || tc.owner == "prepare" {
				kind, payload := "instance_update", `{"preview_id":"preview"}`
				if tc.owner == "prepare" {
					kind, payload = "instance_update_prepare", `{"request":{"manual":{"item":"`+st.ID+`"}}}`
				}
				exec("INSERT INTO jobs(id,instance_id,kind,state,payload,created_at) VALUES('job','alpha',?,?,?,'now')", kind, tc.state, payload)
			}
			if tc.marker != "" {
				if err := os.WriteFile(filepath.Join(f.dirs.State, tc.marker), []byte("uncertain blocker preserved"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.expire(t.Context(), "alpha"); err != nil {
				t.Fatal(err)
			}
			file, err := s.openStage(t.Context(), "alpha", st)
			if tc.keep {
				if err != nil {
					t.Fatalf("required stage lost: %v", err)
				}
				file.Close()
			} else {
				if err == nil {
					file.Close()
					t.Fatal("expired stage remains pinned by historical evidence")
				}
				if _, err := os.Stat(filepath.Join(f.dirs.State, "modpack", st.ID+".blob")); !os.IsNotExist(err) {
					t.Fatal("expired bytes retained", err)
				}
				var remaining int
				if err := database.QueryRow("SELECT count(*) FROM modpack_stages WHERE id=?", st.ID).Scan(&remaining); err != nil || remaining != 0 {
					t.Fatal("expired stage still charged", remaining, err)
				}
			}
			var history int
			if err := database.QueryRow("SELECT count(*) FROM update_previews WHERE id='preview'").Scan(&history); err != nil || history != 1 {
				t.Fatal("preview evidence changed", history, err)
			}
			if tc.owner == "transaction" {
				if err := database.QueryRow("SELECT count(*) FROM update_transactions WHERE id='transaction'").Scan(&history); err != nil || history != 1 {
					t.Fatal("transaction history changed", history, err)
				}
			}
		})
	}
}

// Quota reservations model interrupted/finished artifacts without allocating
// GiB. Actual stages above independently verify retained bytes remain readable.
func TestUpdateStageQuotaReclaimsAcrossInstances(t *testing.T) {
	for _, tc := range []struct {
		name         string
		reservations int
		size         int64
		global       bool
	}{
		{name: "instance byte quota", reservations: 8, size: 1 << 30},
		{name: "global quota frees another instance", reservations: 16, size: 1 << 30, global: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPackFixture(t)
			s, database := f.s, f.database
			if err := f.q.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := database.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('bravo','Bravo','vanilla','1.20.1',17,1024,25566)")
			exec("INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('charlie','Charlie','vanilla','1.20.1',17,1024,25567)")
			target := "alpha"
			if tc.global {
				target = "charlie"
				dirs, err := s.paths.For(target)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.MkdirAll(dirs.State, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for cycle := 0; cycle < 3; cycle++ {
				for i := 0; i < tc.reservations; i++ {
					id := fmt.Sprintf("%032x", cycle*tc.reservations+i+1)
					owner := "alpha"
					if tc.global && i%2 == 1 {
						owner = "bravo"
					}
					dirs, err := s.paths.For(owner)
					if err != nil {
						t.Fatal(err)
					}
					if err = os.MkdirAll(dirs.State, 0700); err != nil {
						t.Fatal(err)
					}
					exec("INSERT INTO modpack_stages(id,instance_id,actor_id,sha256,size,created_at) VALUES(?,?,1,'',?,'2000-01-01T00:00:00Z')", id, owner, tc.size)
					exec("INSERT INTO update_previews(id,instance_id,actor_id,private_json,created_at,expires_at) VALUES(?,?,1,?,'2000-01-01T00:00:00Z','2000-01-01T00:00:00Z')", id, owner, `{"receipt":"`+id+`"}`)
					if i%2 == 1 {
						exec("INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,journal_json,updated_at) VALUES(?,?,?,'finished-job',1,'update','complete','protected-backup',?,'now')", id, owner, id, `{"receipt":"`+id+`"}`)
					}
				}
				st, err := s.stage(t.Context(), target, 1, strings.NewReader("bytes"), 5)
				if err != nil {
					t.Fatalf("cycle %d: expired receipts exhausted admission: %v", cycle, err)
				}
				file, err := s.openStage(t.Context(), target, st)
				if err != nil {
					t.Fatal(err)
				}
				file.Close()
				exec("UPDATE modpack_stages SET created_at='2000-01-01T00:00:00Z'")
			}
			var count int
			if err := database.QueryRow("SELECT count(*) FROM update_previews").Scan(&count); err != nil || count != 3*tc.reservations {
				t.Fatal("expiry removed preview history", count, err)
			}
			if err := database.QueryRow("SELECT count(*) FROM update_transactions").Scan(&count); err != nil || count != 3*(tc.reservations/2) {
				t.Fatal("expiry removed transaction history", count, err)
			}

		})
	}
}

// A job must acquire durable ownership before receipt unlink begins, or wait
// until after cleanup commits. It cannot publish inside that interval.
func TestStageCleanupSerializesQueuedReferencePublication(t *testing.T) {
	f := newPackFixture(t)
	s, database := f.s, f.database
	if err := f.q.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := s.stage(t.Context(), "alpha", 1, strings.NewReader("bytes"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("UPDATE modpack_stages SET created_at='2000-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	publication := make(chan error, 1)
	observedWait := false
	s.fault = func(phase string) error {
		if phase != "stage_cleanup" {
			return nil
		}
		baseline := database.Stats().WaitCount
		go func() {
			_, err := database.ExecContext(ctx, "INSERT INTO jobs(id,instance_id,kind,state,payload,created_at) VALUES('racing-job','alpha','instance_update_prepare','queued',?,'now')", `{"request":{"manual":{"item":"`+st.ID+`"}}}`)
			publication <- err
		}()
		for database.Stats().WaitCount == baseline {
			select {
			case err := <-publication:
				return fmt.Errorf("queued reference published before receipt unlink completed: %v", err)
			case <-ctx.Done():
				return ctx.Err()
			default:
				runtime.Gosched()
			}
		}
		observedWait = true
		if _, err := os.Stat(filepath.Join(f.dirs.State, "modpack", st.ID+".blob")); err != nil {
			return err
		}
		return nil
	}
	s.mu.Lock()
	err = s.cleanupStages(ctx, "alpha", true)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !observedWait {
		t.Fatal("cleanup never exercised queued publication boundary")
	}
	if err := <-publication; err != nil {
		t.Fatal(err)
	}
	var count int
	if err = database.QueryRow("SELECT count(*) FROM modpack_stages WHERE id=?", st.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired receipt was not reclaimed before queued publication", count, err)
	}
	if _, err = os.Stat(filepath.Join(f.dirs.State, "modpack", st.ID+".blob")); !os.IsNotExist(err) {
		t.Fatal("expired artifact remains", err)
	}
}

func TestStageCleanupRetriesAfterMetadataFailure(t *testing.T) {
	f := newPackFixture(t)
	s, database := f.s, f.database
	if err := f.q.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := s.stage(t.Context(), "alpha", 1, strings.NewReader("bytes"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("UPDATE modpack_stages SET created_at='2000-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("CREATE TRIGGER cleanup_failure BEFORE DELETE ON modpack_stages BEGIN SELECT RAISE(ABORT,'injected metadata failure'); END"); err != nil {
		t.Fatal(err)
	}
	cleanup := func() error { s.mu.Lock(); defer s.mu.Unlock(); return s.cleanupStages(t.Context(), "alpha", true) }
	if err = cleanup(); err == nil {
		t.Fatal("partial filesystem cleanup reported success")
	}
	var count int
	if err = database.QueryRow("SELECT count(*) FROM modpack_stages WHERE id=?", st.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed metadata deletion lost quota reservation", count, err)
	}
	if _, err = os.Stat(filepath.Join(f.dirs.State, "modpack", st.ID+".blob")); !os.IsNotExist(err) {
		t.Fatal("fixture did not unlink before injected metadata failure", err)
	}
	if _, err = database.Exec("DROP TRIGGER cleanup_failure"); err != nil {
		t.Fatal(err)
	}
	if err = cleanup(); err != nil {
		t.Fatal("cleanup could not retry already absent bytes", err)
	}
	if err = database.QueryRow("SELECT count(*) FROM modpack_stages WHERE id=?", st.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("retry failed to release quota reservation", count, err)
	}
}
