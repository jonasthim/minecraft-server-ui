package instance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type pendingGuardFunc func(context.Context, string, func() error) error

func (f pendingGuardFunc) WithPendingSettings(ctx context.Context, id string, fn func() error) error {
	return f(ctx, id, fn)
}
func pendingFixture(t *testing.T) (*Service, Detail, *testGuard) {
	t.Helper()
	s, _, native := setup(t)
	if _, err := s.db.Exec(`INSERT INTO users(id,username,role,created_at,updated_at) VALUES(1,'admin','admin','now','now'),(2,'other-admin','admin','now','now')`); err != nil {
		t.Fatal(err)
	}
	s.pendingGuard = pendingGuardFunc(func(_ context.Context, _ string, fn func() error) error { return fn() })
	d, err := s.Create(t.Context(), admin, draft("alpha", 25565))
	if err != nil {
		t.Fatal(err)
	}
	return s, d, native
}
func pendingInput(d Detail) SavePendingSettings {
	return SavePendingSettings{PendingSettingsDraft: PendingSettingsDraft{Name: "Desired name", MemoryMiB: 2048, Config: d.Config}, ExpectedInstanceUpdatedAt: d.Instance.UpdatedAt.Format(time.RFC3339Nano)}
}
func TestPendingSettingsSaveWhileRunningIsDBOnly(t *testing.T) {
	s, d, native := pendingFixture(t)
	ctx := t.Context()
	dirs, _ := s.paths.For("alpha")
	file := filepath.Join(dirs.Server, "server.properties")
	before, _ := os.ReadFile(file)
	native.blocked = true
	state, err := s.SavePendingSettings(ctx, admin, "alpha", pendingInput(d))
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != 1 || state.Draft == nil || state.Stale {
		t.Fatalf("state: %+v", state)
	}
	active, err := s.Get(ctx, "alpha")
	if err != nil || active.Instance.Name != d.Instance.Name || !active.Instance.UpdatedAt.Equal(d.Instance.UpdatedAt) {
		t.Fatalf("active changed: %+v %v", active, err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("live properties changed")
	}
	if _, err = os.Stat(filepath.Join(dirs.Root, configIntentPath)); !os.IsNotExist(err) {
		t.Fatalf("intent created: %v", err)
	}
	var logs int
	if err = s.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='instance.pending_settings.save' AND details_json<>'{}'`).Scan(&logs); err != nil || logs != 0 {
		t.Fatalf("audit config leak: %d %v", logs, err)
	}
}
func TestPendingSettingsCASAuthorizationDiscardAndValidation(t *testing.T) {
	s, d, _ := pendingFixture(t)
	ctx := t.Context()
	in := pendingInput(d)
	zero, err := s.GetPendingSettings(ctx, "alpha")
	if err != nil || zero.Revision != 0 || zero.Draft != nil {
		t.Fatal(zero, err)
	}
	if _, err = s.SavePendingSettings(ctx, domain.User{ID: 1, Role: domain.RoleViewer}, "alpha", in); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal(err)
	}
	one, err := s.SavePendingSettings(ctx, admin, "alpha", in)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.SavePendingSettings(ctx, admin, "alpha", in)
	if err != nil || retry.Revision != one.Revision {
		t.Fatal(retry, err)
	}
	in.Name = "Conflicting"
	if _, err = s.SavePendingSettings(ctx, admin, "alpha", in); domain.AsError(err).Code != domain.CodeConflict {
		t.Fatal(err)
	}
	if _, err = s.Update(ctx, admin, "alpha", Draft{Instance: d.Instance, Config: d.Config}); domain.AsError(err).Code != domain.CodeConflict {
		t.Fatal("direct update ignored draft", err)
	}
	if _, err = s.DiscardPendingSettings(ctx, admin, "alpha", 0); domain.AsError(err).Code != domain.CodeConflict {
		t.Fatal(err)
	}
	empty, err := s.DiscardPendingSettings(ctx, admin, "alpha", 1)
	if err != nil || empty.Revision != 2 || empty.Draft != nil {
		t.Fatal(empty, err)
	}
	in.ExpectedRevision = 2
	in.Config.LevelName = "other"
	if _, err = s.SavePendingSettings(ctx, admin, "alpha", in); domain.AsError(err).Code != domain.CodeConflict {
		t.Fatal(err)
	}
	in.Config = d.Config
	in.Config.Extra = map[string]string{"api-token": "private"}
	if _, err = s.SavePendingSettings(ctx, admin, "alpha", in); domain.AsError(err).Code != domain.CodeValidationFailed {
		t.Fatal(err)
	}
	in = pendingInput(d)
	in.ExpectedRevision = 2
	if _, err = s.db.Exec(`UPDATE users SET disabled=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SavePendingSettings(ctx, admin, "alpha", in); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("revoked admin accepted", err)
	}
}
func TestPendingSettingsStaleAndCorruptStoredDraft(t *testing.T) {
	s, d, _ := pendingFixture(t)
	ctx := t.Context()
	in := pendingInput(d)
	if _, err := s.SavePendingSettings(ctx, admin, "alpha", in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE instances SET updated_at='2099-01-01T00:00:00Z' WHERE id='alpha'`); err != nil {
		t.Fatal(err)
	}
	state, err := s.GetPendingSettings(ctx, "alpha")
	if err != nil || !state.Stale {
		t.Fatal(state, err)
	}
	if _, err = s.SavePendingSettings(ctx, admin, "alpha", in); domain.AsError(err).Code != domain.CodeConflict {
		t.Fatal("stale retry accepted", err)
	}
	if _, err = s.db.Exec(`UPDATE instance_pending_settings SET draft_json='{"unknown":true}' WHERE instance_id='alpha'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetPendingSettings(ctx, "alpha"); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatal("corrupt draft accepted", err)
	}
}

func TestPendingSettingsCurrentJobCapabilityAndQueuedSuccessor(t *testing.T) {
	for _, mode := range []string{"lifecycle", "crash_recovery", "foreign_kind", "durable_stop", "copy", "forged", "copied_session", "foreign_session"} {
		t.Run(mode, func(t *testing.T) {
			s, d, native := pendingFixture(t)
			ctx := t.Context()
			q := jobs.New(s.db, nil)
			guard := &composedJobGuard{native: native}
			s.jobGuard = guard
			s.pendingGuard = pendingGuardFunc(q.WithIdle)
			kind, payload := "lifecycle", `{"operation":"start"}`
			switch mode {
			case "crash_recovery":
				kind = "crash_recovery"
				payload = `{}`
			case "foreign_kind":
				kind = "instance_update"
			case "durable_stop":
				payload = `{"operation":"stop"}`
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			result := make(chan error, 1)
			var retained *StoppedSession
			var execution *jobs.Execution
			if err := q.Register(kind, func(ctx context.Context, e *jobs.Execution) error {
				execution = e
				return s.WithStoppedJob(ctx, e, func(session *StoppedSession) error {
					retained = session
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					// Mutable public fields never authorize another operation or instance.
					e.Job.InstanceID = "foreign"
					e.Job.Kind = "lifecycle"
					e.Job.ID = "public-forgery"
					e.Payload = []byte(`{"operation":"start"}`)
					candidate := e
					switch mode {
					case "copy":
						copy := *e
						candidate = &copy
					case "forged":
						candidate = &jobs.Execution{}
					case "copied_session":
						copy := *session
						session = &copy
					case "foreign_session":
						return s.withStoppedSession(ctx, "alpha", func(foreign *StoppedSession) error {
							err := foreign.ApplyPendingSettings(ctx, e)
							result <- err
							return nil
						})
					}
					err := session.ApplyPendingSettings(ctx, candidate)
					result <- err
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
			if err := q.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer q.Stop(context.Background())
			if _, err := s.SavePendingSettings(ctx, admin, "alpha", pendingInput(d)); err != nil {
				t.Fatal(err)
			}
			j, err := q.Enqueue(ctx, jobs.Request{InstanceID: "alpha", Kind: kind, Payload: []byte(payload), RequestedBy: 1})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("session missing")
			}
			if mode == "lifecycle" {
				// Same-instance queued successors are serialized and must not block the owned
				// current job's atomic config commit. A concurrent save must still refuse.
				if _, err = s.SavePendingSettings(ctx, admin, "alpha", pendingInput(d)); domain.AsError(err).Code != domain.CodeInstanceBusy {
					t.Fatal("save admitted during job", err)
				}
				if _, err = s.db.Exec(`INSERT INTO jobs(id,instance_id,kind,state,created_at,payload,requested_by) VALUES('successor','alpha','not_registered','queued','now','{}',1)`); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			waitInstanceJob(t, q, j.ID)
			err = <-result
			if mode == "lifecycle" || mode == "crash_recovery" {
				if err != nil {
					t.Fatal(err)
				}
			} else if domain.AsError(err).Code != domain.CodeForbidden {
				t.Fatal("invalid capability accepted", err)
			}
			if err = retained.ApplyPendingSettings(ctx, execution); domain.AsError(err).Code != domain.CodeForbidden {
				t.Fatal("expired session accepted", err)
			}
			active, err := s.Get(ctx, "alpha")
			if err != nil {
				t.Fatal(err)
			}
			applied := mode == "lifecycle" || mode == "crash_recovery"
			if (active.Instance.Name == "Desired name") != applied {
				t.Fatal("wrong apply outcome", active)
			}
			if guard.nativeCalls.Load() != 1 {
				t.Fatal("nested native lock", guard.nativeCalls.Load())
			}
		})
	}
}
func TestPendingSettingsConcurrentCASAndAtomicAudit(t *testing.T) {
	s, d, _ := pendingFixture(t)
	ctx := t.Context()
	var wg sync.WaitGroup
	var successful atomic.Int32
	for _, name := range []string{"First", "Second"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			in := pendingInput(d)
			in.Name = name
			_, err := s.SavePendingSettings(ctx, admin, "alpha", in)
			if err == nil {
				successful.Add(1)
			} else if domain.AsError(err).Code != domain.CodeConflict {
				t.Error(err)
			}
		}(name)
	}
	wg.Wait()
	if successful.Load() != 1 {
		t.Fatal("lost compare-and-swap", successful.Load())
	}
	if _, err := s.DiscardPendingSettings(ctx, admin, "alpha", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER pending_save_audit_failure BEFORE INSERT ON audit_log WHEN new.action='instance.pending_settings.save' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	in := pendingInput(d)
	in.ExpectedRevision = 2
	if _, err := s.SavePendingSettings(ctx, admin, "alpha", in); err == nil {
		t.Fatal("audit failure hidden")
	}
	state, err := s.GetPendingSettings(ctx, "alpha")
	if err != nil || state.Revision != 2 || state.Draft != nil {
		t.Fatal("failed audit changed draft", state, err)
	}
}
func TestPendingSettingsSaveFailsClosedWithoutGuardOrAtRevisionLimit(t *testing.T) {
	s, d, _ := pendingFixture(t)
	ctx := t.Context()
	guard := s.pendingGuard
	s.pendingGuard = nil
	if _, err := s.SavePendingSettings(ctx, admin, "alpha", pendingInput(d)); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatal(err)
	}
	s.pendingGuard = guard
	if _, err := s.SavePendingSettings(ctx, admin, "alpha", pendingInput(d)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE instance_pending_settings SET revision=? WHERE instance_id='alpha'`, maxPendingRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DiscardPendingSettings(ctx, admin, "alpha", maxPendingRevision); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatal("overflow admitted", err)
	}
	in := pendingInput(d)
	in.ExpectedRevision = maxPendingRevision + 1
	if _, err := s.SavePendingSettings(ctx, admin, "alpha", in); domain.AsError(err).Code != domain.CodeValidationFailed {
		t.Fatal(err)
	}
}

func TestPendingSettingsStoredFieldsMustBeCompleteAndNonNull(t *testing.T) {
	for _, field := range []string{"name", "memory_mib", "config", "online_mode", "pvp", "extra"} {
		for _, null := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/null=%t", field, null), func(t *testing.T) {
				s, d, _ := pendingFixture(t)
				ctx := t.Context()
				in := pendingInput(d)
				if _, err := s.SavePendingSettings(ctx, admin, "alpha", in); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(in.PendingSettingsDraft)
				var object map[string]any
				if err := json.Unmarshal(raw, &object); err != nil {
					t.Fatal(err)
				}
				target := object
				if field == "online_mode" || field == "pvp" || field == "extra" {
					target = object["config"].(map[string]any)
				}
				if null {
					target[field] = nil
				} else {
					delete(target, field)
				}
				raw, _ = json.Marshal(object)
				if _, err := s.db.Exec(`UPDATE instance_pending_settings SET draft_json=? WHERE instance_id='alpha'`, string(raw)); err != nil {
					t.Fatal(err)
				}
				if _, err := s.GetPendingSettings(ctx, "alpha"); domain.AsError(err).Code != domain.CodeUnavailable {
					t.Fatal("incomplete stored draft accepted", err)
				}
			})
		}
	}
}
func TestPendingSettingsVersionSelectionPreservesStaleDraftAndAllowsDeliberateRebase(t *testing.T) {
	s, d, _ := pendingFixture(t)
	ctx := t.Context()
	if _, err := s.SavePendingSettings(ctx, admin, "alpha", pendingInput(d)); err != nil {
		t.Fatal(err)
	}
	if err := runSelection(t, s, "1.20.1", false); err != nil {
		t.Fatal(err)
	}
	state, err := s.GetPendingSettings(ctx, "alpha")
	if err != nil || state.Draft == nil || !state.Stale || state.Revision != 1 {
		t.Fatal(state, err)
	}
	next, err := s.Get(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	in := pendingInput(next)
	in.ExpectedRevision = state.Revision
	state, err = s.SavePendingSettings(ctx, admin, "alpha", in)
	if err != nil || state.Stale || state.Revision != 2 {
		t.Fatal(state, err)
	}
}

func TestPendingSettingsAnotherAdminCanReauthorizeSameDesiredDraft(t *testing.T) {
	s, d, _ := pendingFixture(t)
	ctx := t.Context()
	in := pendingInput(d)
	if _, err := s.SavePendingSettings(ctx, admin, "alpha", in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE users SET disabled=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	in.ExpectedRevision = 1
	state, err := s.SavePendingSettings(ctx, domain.User{ID: 2, Username: "other-admin", Role: domain.RoleAdmin}, "alpha", in)
	if err != nil || state.UpdatedBy != 2 || state.Revision != 2 {
		t.Fatal("deliberate resave did not authorize new originator", state, err)
	}
}

func TestPendingSettingsRejectsOversizedSerializedDraftAtomically(t *testing.T) {
	for _, prior := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior=%t", prior), func(t *testing.T) {
			s, d, _ := pendingFixture(t)
			ctx := t.Context()
			if prior {
				if _, err := s.SavePendingSettings(ctx, admin, "alpha", pendingInput(d)); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.GetPendingSettings(ctx, "alpha")
			if err != nil {
				t.Fatal(err)
			}
			var auditsBefore int
			if err := s.db.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&auditsBefore); err != nil {
				t.Fatal(err)
			}
			in := pendingInput(d)
			in.ExpectedRevision = before.Revision
			in.Name = "Oversized replacement"
			in.Config.Extra = map[string]string{}
			for i := range 100 {
				in.Config.Extra[fmt.Sprintf("mod.option.%03d", i)] = strings.Repeat("<", 5000)
			}
			raw, err := json.Marshal(in.PendingSettingsDraft)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) <= 2<<20 {
				t.Fatalf("fixture does not exceed serialized limit: %d", len(raw))
			}
			if _, err := s.SavePendingSettings(ctx, admin, "alpha", in); domain.AsError(err).Code != domain.CodeValidationFailed {
				t.Fatalf("oversized serialized draft accepted: %v", err)
			}
			after, err := s.GetPendingSettings(ctx, "alpha")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed prior draft: before=%+v after=%+v err=%v", before, after, err)
			}
			var auditsAfter, rows int
			if err := s.db.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&auditsAfter); err != nil || auditsAfter != auditsBefore {
				t.Fatalf("refusal changed audits: before=%d after=%d err=%v", auditsBefore, auditsAfter, err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM instance_pending_settings WHERE instance_id='alpha'`).Scan(&rows); err != nil || (rows == 1) != prior {
				t.Fatalf("refusal changed pending row existence: rows=%d err=%v", rows, err)
			}
			if _, err := s.DiscardPendingSettings(ctx, admin, "alpha", before.Revision); err != nil {
				t.Fatalf("prior state cannot be discarded: %v", err)
			}
		})
	}
}
