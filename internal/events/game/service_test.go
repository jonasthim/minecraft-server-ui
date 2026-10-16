package game

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

type logSource struct {
	h     *console.History
	state console.State
	err   error
}

func (l *logSource) History(ctx context.Context, id string, after int64, limit int) (console.Page, error) {
	return l.h.Read(ctx, id, after, limit)
}
func (l *logSource) Status(context.Context, string) (console.State, error) { return l.state, l.err }

type instanceSource struct{ version string }

func (i *instanceSource) Get(context.Context, string) (instance.Detail, error) {
	return instance.Detail{Instance: domain.Instance{MinecraftVersion: i.version}}, nil
}
func fixture(t *testing.T) (*Service, *sql.DB, *logSource, *instanceSource) {
	t.Helper()
	d, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	_, err = d.Exec(`INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('alpha','Alpha','vanilla','1.21.9',21,1024,25565),('beta','Beta','vanilla','1.12.2',8,1024,25566)`)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := instance.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logs := &logSource{h: console.NewHistory(d, paths), state: console.State{LogsAvailable: true, LogsObservedAt: time.Now()}}
	instances := &instanceSource{version: "1.21.9"}
	s := New(d, logs, instances)
	return s, d, logs, instances
}
func line(t *testing.T, d *sql.DB, id, body string, at time.Time, gap bool) int64 {
	t.Helper()
	r, err := d.Exec(`INSERT INTO console_lines(instance_id,created_at,message,gap) VALUES(?,?,?,?)`, id, at.UTC().Format(time.RFC3339Nano), body, gap)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := r.LastInsertId()
	return n
}
func TestReplayAtomicCursorAndProvenance(t *testing.T) {
	s, d, _, _ := fixture(t)
	stamp := time.Now().UTC()
	source := line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: Alex drowned", stamp, false)
	if _, err := d.Exec(`CREATE TRIGGER reject_cursor BEFORE UPDATE ON game_event_cursors BEGIN SELECT RAISE(ABORT,'injected cursor failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Poll(t.Context(), "alpha"); err == nil {
		t.Fatal("cursor failure hidden")
	}
	var count int
	if err := d.QueryRow(`SELECT COUNT(*) FROM game_events`).Scan(&count); err != nil || count != 0 {
		t.Fatal("event escaped rollback", count, err)
	}
	if _, err := d.Exec(`DROP TRIGGER reject_cursor`); err != nil {
		t.Fatal(err)
	}
	if err := s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	page, err := s.History(t.Context(), "alpha", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("replay events %#v", page.Events)
	}
	e := page.Events[0]
	if e.SourceCursor != source || !e.ObservedAt.Equal(stamp) || e.ServerTime != nil || e.SourceVersion != "unknown" || e.Player.UUID != "" || e.Player.Authority != "name_only" || e.Confidence != "native_log_pattern" || !page.Gap {
		t.Fatalf("false provenance %#v %#v", e, page)
	}
	restarted := New(d, s.logs, s.instances)
	if err := restarted.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	page, err = restarted.History(t.Context(), "alpha", 0, 200)
	if err != nil || len(page.Events) != 1 {
		t.Fatal(page, err)
	}
}
func TestGapsCapabilitiesAndIdentitySeparation(t *testing.T) {
	s, d, logs, instances := fixture(t)
	now := time.Now()
	line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: Alex drowned", now, false)
	line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: <Other> Alex drowned", now, false)
	line(t, d, "alpha", "[Console history gap: raw logs rotated.]", now, true)
	line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: Alex has made the advancement [redacted]", now, false)
	line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: Alex has made the advancement [Stone Age]", now, false)
	logs.state.LogsAvailable = false
	instances.version = "26.3.1"
	if err := s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	p, err := s.History(t.Context(), "alpha", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if p.Coverage.Available || p.Coverage.ParserCapability != "unavailable_26_3" || len(p.Events) != 5 {
		t.Fatalf("coverage/events %#v", p)
	}
	for _, e := range p.Events {
		if e.Player.UUID != "" || e.SourceVersion != "unknown" {
			t.Fatal(e)
		}
	}
	instances.version = "1.12.2"
	logs.state.LogsAvailable = true
	if err = s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	p, err = s.History(t.Context(), "alpha", 0, 200)
	if err != nil || !p.Coverage.Available || p.Coverage.ParserCapability != "static_reviewed_patterns" {
		t.Fatal(p, err)
	}
	instances.version = "1.16.5"
	if err = s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	p, _ = s.History(t.Context(), "alpha", 0, 200)
	if p.Coverage.ParserCapability != "unverified_version" || p.Coverage.Available {
		t.Fatal(p)
	}
}
func TestRetentionPaginationAndForeignCursor(t *testing.T) {
	s, d, _, _ := fixture(t)
	now := time.Now().UTC()
	old := now.Add(-31 * 24 * time.Hour)
	line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: Alex drowned", old, false)
	line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: Steve drowned", now, false)
	if err := s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	p, err := s.History(t.Context(), "alpha", 0, 1)
	if err != nil || p.RetentionFloor == 0 || len(p.Events) != 1 || p.Events[0].Player.Name != "Steve" {
		t.Fatal(p, err)
	}
	line(t, d, "beta", "[12:34:56] [Server thread/INFO]: Alex drowned", now, false)
	if err = s.Poll(t.Context(), "beta"); err != nil {
		t.Fatal(err)
	}
	other, _ := s.History(t.Context(), "beta", 0, 200)
	p, err = s.History(t.Context(), "alpha", other.NextCursor, 200)
	if err != nil || len(p.Events) != 1 || !p.Gap {
		t.Fatal(p, err)
	}
	p, err = s.History(t.Context(), "alpha", 1<<40, 200)
	if err != nil || len(p.Events) != 1 {
		t.Fatal(p, err)
	}
	for _, v := range []struct {
		id    string
		after int64
		limit int
	}{{"../x", 0, 1}, {"alpha", -1, 1}, {"alpha", 0, 201}} {
		if _, err = s.History(t.Context(), v.id, v.after, v.limit); domain.AsError(err).Code != domain.CodeValidationFailed {
			t.Fatal(v, err)
		}
	}
	_, err = d.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<20001) INSERT INTO game_events(instance_id,source_key,source_cursor,observed_at,event_json) SELECT 'alpha','test:'||x,x,?,? FROM n`, now.Format(time.RFC3339Nano), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = d.QueryRow(`SELECT COUNT(*) FROM game_events WHERE instance_id='alpha'`).Scan(&count); err != nil || count != 20000 {
		t.Fatal(count, err)
	}
}
func TestSourceLossAndServiceFailure(t *testing.T) {
	s, d, logs, _ := fixture(t)
	now := time.Now()
	line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: Alex drowned", now, false)
	if err := s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`DELETE FROM console_lines; INSERT INTO console_cursors(instance_id,byte_offset,boundary_hash,pruned_before) VALUES('alpha',0,'',100)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	p, err := s.History(t.Context(), "alpha", 0, 200)
	if err != nil || len(p.Events) != 3 || p.Events[1].Kind != "source_gap" || p.Events[2].Kind != "source_gap" {
		t.Fatal(p, err)
	}
	logs.err = errors.New("private path /secret/raw")
	logs.state.LogsAvailable = false
	if err = s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	p, _ = s.History(t.Context(), "alpha", 0, 200)
	if strings.Contains(p.Coverage.Reason, "/secret") {
		t.Fatal(p)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`DROP TABLE game_events`); err != nil {
		t.Fatal(err)
	}
	if err = s.Run(t.Context()); err == nil {
		t.Fatal("unexpected service failure invisible")
	}
}

type blockingLogs struct{ entered chan struct{} }

func (l *blockingLogs) Status(context.Context, string) (console.State, error) {
	return console.State{}, nil
}
func (l *blockingLogs) History(ctx context.Context, _ string, _ int64, _ int) (console.Page, error) {
	close(l.entered)
	<-ctx.Done()
	return console.Page{}, ctx.Err()
}
func TestRunCancellationDrainsActiveRead(t *testing.T) {
	s, _, _, _ := fixture(t)
	logs := &blockingLogs{entered: make(chan struct{})}
	s.logs = logs
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	select {
	case <-logs.entered:
	case <-time.After(time.Second):
		t.Fatal("source read never started")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("source read did not drain after cancellation")
	}
}
func Test26_3ReviewedPartialCapability(t *testing.T) {
	s, d, _, instances := fixture(t)
	instances.version = "26.3"
	line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: System chat: Alex drowned", time.Now(), false)
	line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: System chat: Alex has made the advancement [Stone Age]", time.Now(), false)
	if err := s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	p, err := s.History(t.Context(), "alpha", 0, 200)
	if err != nil || !p.Coverage.Available || p.Coverage.ParserCapability != "static_reviewed_patterns" || len(p.Events) != 2 || !p.Gap || !strings.Contains(p.Coverage.Reason, "team death-message visibility") {
		t.Fatal(p, err)
	}
}
func TestDistinctRepeatedEventsAndBoundedPagination(t *testing.T) {
	s, d, _, _ := fixture(t)
	for i := 0; i < 3; i++ {
		line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: Alex drowned", time.Now(), false)
	}
	if err := s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	p, err := s.History(t.Context(), "alpha", 0, 2)
	if err != nil || !p.More || len(p.Events) != 2 {
		t.Fatal(p, err)
	}
	next, err := s.History(t.Context(), "alpha", p.NextCursor, 2)
	if err != nil || next.More || len(next.Events) != 1 || next.Events[0].SourceCursor <= p.Events[1].SourceCursor {
		t.Fatal(next, err)
	}
}

func TestInitialConsoleRetentionBecomesHistoryGap(t *testing.T) {
	s, d, _, _ := fixture(t)
	old := line(t, d, "alpha", "ignored old output", time.Now(), false)
	latest := line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: Alex drowned", time.Now(), false)
	if _, err := d.Exec(`DELETE FROM console_lines WHERE id=?`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO console_cursors(instance_id,byte_offset,boundary_hash,pruned_before) VALUES('alpha',0,'',?)`, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	p, err := s.History(t.Context(), "alpha", 0, 200)
	if err != nil || len(p.Events) != 2 || p.Events[0].Kind != "source_gap" || p.Events[0].SourceCursor != old || p.Events[1].SourceCursor != latest {
		t.Fatal(p, err)
	}
	if err = s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	p, _ = s.History(t.Context(), "alpha", 0, 200)
	if len(p.Events) != 2 {
		t.Fatal("retention gap duplicated", p)
	}
}

type deletingLogs struct {
	Logs
	db *sql.DB
}

func (l deletingLogs) History(ctx context.Context, id string, after int64, limit int) (console.Page, error) {
	p, err := l.Logs.History(ctx, id, after, limit)
	if err != nil {
		return p, err
	}
	_, err = l.db.ExecContext(ctx, `DELETE FROM instances WHERE id=?`, id)
	return p, err
}
func TestDisappearingInstanceIsNotObserverFailure(t *testing.T) {
	s, d, _, _ := fixture(t)
	line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: Alex drowned", time.Now(), false)
	s.logs = deletingLogs{s.logs, d}
	err := s.Poll(t.Context(), "alpha")
	if domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatal("deleted instance is a fatal failure", err)
	}
	var n int
	if err = d.QueryRow(`SELECT COUNT(*) FROM game_events`).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestMissingConsoleOutputRemainsUnavailable(t *testing.T) {
	s, d, _, _ := fixture(t)
	if err := s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	p, err := s.History(t.Context(), "alpha", 0, 200)
	if err != nil || p.Coverage.Available || !strings.Contains(p.Coverage.Reason, "No retained native console output") {
		t.Fatal("empty source presented available", p, err)
	}
	line(t, d, "alpha", "[12:34:56] [Server thread/INFO]: Alex drowned", time.Now(), false)
	if err = s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if err = s.Poll(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	p, err = s.History(t.Context(), "alpha", 0, 200)
	if err != nil || !p.Coverage.Available {
		t.Fatal("quiet previously observed source unavailable", p, err)
	}
}
