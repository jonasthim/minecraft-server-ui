package metrics

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeInstances struct{}

func (fakeInstances) List(context.Context) ([]instance.Detail, error) {
	return []instance.Detail{{Instance: domain.Instance{ID: "alpha"}}}, nil
}
func (fakeInstances) Get(_ context.Context, id string) (instance.Detail, error) {
	if id != "alpha" {
		return instance.Detail{}, domain.E(domain.CodeNotFound, "missing")
	}
	return instance.Detail{}, nil
}

type fakeNative struct {
	id   domain.ProcessIdentity
	cpu  int64
	rss  int64
	fail bool
}

func (f *fakeNative) Identity(context.Context, string) (domain.ProcessIdentity, error) {
	return f.id, nil
}
func (f *fakeNative) Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error) {
	return supervisor.Observation{ProcessRunning: !f.fail}, nil
}
func (f *fakeNative) SampleMetrics(context.Context, domain.ProcessIdentity) (launcher.ProcessCounters, error) {
	if f.fail {
		return launcher.ProcessCounters{}, errors.New("private unavailable")
	}
	return launcher.ProcessCounters{CPUNanos: &f.cpu, ResidentBytes: &f.rss, ObservedAt: time.Now(), Source: "linux_proc"}, nil
}

type fakePlayers struct{ snapshot console.PlayerSnapshot }

func (f *fakePlayers) Snapshot(context.Context, string) (console.PlayerSnapshot, error) {
	return f.snapshot, nil
}
func metricFixture(t *testing.T) (*Service, *time.Time, *fakeNative, *fakePlayers) {
	t.Helper()
	database, e := db.OpenMemory(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { database.Close() })
	_, e = database.Exec(`INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port)VALUES('alpha','Alpha','vanilla','1.21.9',21,1024,25565)`)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	data := t.TempDir()
	paths, e := instance.NewPaths(data)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Join(data, "instances/alpha/server"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(data, "instances/alpha/server/world.dat"), []byte("12345"), 0600); e != nil {
		t.Fatal(e)
	}
	native := &fakeNative{id: domain.ProcessIdentity{InstanceID: "alpha", PID: 200, StartIdentity: "boot:1"}, cpu: int64(time.Second), rss: 1024}
	players := &fakePlayers{snapshot: console.PlayerSnapshot{Known: true, Complete: true, Count: 0, Generation: "200:boot:1", ObservedAt: now}}
	s := New(Config{DB: database, Paths: paths, Instances: fakeInstances{}, Native: native, Players: players, Now: func() time.Time { return now }})
	return s, &now, native, players
}
func TestCPUIdentityUnknownRosterAndHistory(t *testing.T) {
	s, now, native, players := metricFixture(t)
	ctx := t.Context()
	if e := s.Sample(ctx, "alpha"); e != nil {
		t.Fatal(e)
	}
	*now = now.Add(30 * time.Second)
	native.cpu += int64(15 * time.Second)
	players.snapshot.ObservedAt = *now
	if e := s.Sample(ctx, "alpha"); e != nil {
		t.Fatal(e)
	}
	page, e := s.Query(ctx, Query{"alpha", now.Add(-30 * time.Second), now.Add(time.Second), "raw"})
	if e != nil {
		t.Fatal(e)
	}
	if len(page.Points) != 2 || page.Points[0].CPUPercent != nil || page.Points[1].CPUPercent == nil || *page.Points[1].CPUPercent != 50 || page.Points[1].Players == nil || *page.Points[1].Players != 0 || page.Points[1].PrivateBytes != nil || *page.Points[1].DiskBytes != 5 {
		t.Fatalf("unexpected points: %+v", page.Points)
	}
	*now = now.Add(30 * time.Second)
	native.id.StartIdentity = "boot:2"
	players.snapshot.ObservedAt = *now
	native.cpu += int64(time.Second)
	if e = s.Sample(ctx, "alpha"); e != nil {
		t.Fatal(e)
	}
	page, e = s.Query(ctx, Query{"alpha", *now, now.Add(time.Second), "raw"})
	if e != nil {
		t.Fatal(e)
	}
	if page.Points[0].CPUPercent != nil || page.Points[0].Players != nil {
		t.Fatal("recycled JVM or prior-generation roster accepted")
	}
	*now = now.Add(30 * time.Second)
	native.fail = true
	if e = s.Sample(ctx, "alpha"); e != nil {
		t.Fatal(e)
	}
	page, e = s.Query(ctx, Query{"alpha", now.Add(-2 * time.Minute), now.Add(time.Second), "raw"})
	if e != nil {
		t.Fatal(e)
	}
	if !page.Points[0].Gap || page.Points[0].Counts.Samples != 0 || page.Points[len(page.Points)-1].ResidentBytes != nil {
		t.Fatal("unavailable samples became zero or disappeared")
	}
}
func TestRollupCountsIdempotenceAndRetentionBoundaries(t *testing.T) {
	s, now, _, _ := metricFixture(t)
	ctx := t.Context()
	at := *now
	for n := 0; n < 120; n++ {
		v := float64(n)
		memory := int64(1000 + n)
		p := Point{CollectedAt: at.Add(time.Duration(n) * Interval), CPUPercent: &v, ResidentBytes: &memory, DiskBytes: &memory, Players: &v, Counts: Counts{Samples: 1, CPU: 1, Resident: 1, Disk: 1, Players: 1}, Expected: 1, Sources: []Source{}}
		if e := s.store(ctx, "alpha", p); e != nil {
			t.Fatal(e)
		}
		if e := s.store(ctx, "alpha", p); e != nil {
			t.Fatal(e)
		}
	}
	*now = at.Add(time.Hour)
	page, e := s.Query(ctx, Query{"alpha", at, *now, "hourly"})
	if e != nil {
		t.Fatal(e)
	}
	point := page.Points[0]
	if point.Gap || point.Counts.Samples != 120 || point.Counts.CPU != 120 || *point.CPUPercent != 59.5 || *point.ResidentBytes != 1060 {
		t.Fatalf("dishonest hourly: %+v", point)
	}
	// Missing one field is excluded from its mean and count, never averaged as zero.
	missing := Point{CollectedAt: *now, Counts: Counts{Samples: 1}, Expected: 1, Gap: true, Sources: []Source{}}
	if e = s.store(ctx, "alpha", missing); e != nil {
		t.Fatal(e)
	}
	*now = now.Add(time.Hour)
	page, e = s.Query(ctx, Query{"alpha", at.Add(time.Hour), *now, "hourly"})
	if e != nil {
		t.Fatal(e)
	}
	if page.Points[0].CPUPercent != nil || page.Points[0].Counts.CPU != 0 || !page.Points[0].Gap {
		t.Fatal("absent value was averaged")
	}
	rawCut := now.Add(-RawRetention).Unix()
	hourCut := now.Add(-HourRetention).Unix()
	for _, slot := range []int64{rawCut - 1, rawCut} {
		if _, e = s.cfg.DB.Exec(`INSERT INTO metric_samples(instance_id,slot,sample_json)VALUES('alpha',?,'{}')`, slot); e != nil {
			t.Fatal(e)
		}
	}
	for _, slot := range []int64{hourCut - 1, hourCut} {
		if _, e = s.cfg.DB.Exec(`INSERT INTO metric_hours(instance_id,slot,aggregate_json)VALUES('alpha',?,'{}')`, slot); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Retain(ctx); e != nil {
		t.Fatal(e)
	}
	for _, test := range []struct {
		table string
		cut   int64
	}{{"metric_samples", rawCut}, {"metric_hours", hourCut}} {
		var count int
		if e = s.cfg.DB.QueryRow(`SELECT COUNT(*) FROM `+test.table+` WHERE slot BETWEEN ? AND ?`, test.cut-1, test.cut).Scan(&count); e != nil || count != 1 {
			t.Fatal(test, count, e)
		}
	}
}
func TestQueryLimitsDiskSymlinkAndCancellation(t *testing.T) {
	s, now, _, _ := metricFixture(t)
	ctx := t.Context()
	for _, q := range []Query{{"../alpha", now.Add(-time.Hour), *now, "raw"}, {"alpha", now.Add(-8 * 24 * time.Hour), *now, "raw"}, {"alpha", now.Add(-9 * time.Hour), *now, "raw"}, {"alpha", now.Add(-time.Hour), *now, "other"}, {"alpha", *now, *now, "raw"}} {
		if _, e := s.Query(ctx, q); domain.AsError(e).Code != domain.CodeValidationFailed {
			t.Fatal(q, e)
		}
	}
	paths, e := s.cfg.Paths.For("alpha")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(t.TempDir(), filepath.Join(paths.Server, "escape")); e != nil {
		t.Fatal(e)
	}
	if _, e = s.disk(ctx, "alpha"); e == nil {
		t.Fatal("symlink disk scan accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = s.disk(canceled, "alpha"); e == nil {
		t.Fatal("ignored cancellation")
	}
	if e = s.Run(canceled); e != nil {
		t.Fatal(e)
	}
}

func TestDeletedInstanceDoesNotKillCollector(t *testing.T) {
	s, _, native, _ := metricFixture(t)
	native.fail = true
	// Deletion can race an already-admitted sample. Its transaction checks the
	// authoritative instance row, avoiding a fatal foreign-key error or ghost row.
	if _, e := s.cfg.DB.Exec(`DELETE FROM instances WHERE id='alpha'`); e != nil {
		t.Fatal(e)
	}
	if e := s.Sample(t.Context(), "alpha"); e != nil {
		t.Fatal("deletion killed sampler", e)
	}
	var count int
	if e := s.cfg.DB.QueryRow(`SELECT COUNT(*) FROM metric_samples`).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
}
