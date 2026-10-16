package mapview

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/mapdata"
	"sync/atomic"
	"testing"
	"time"
)

type terrain struct {
	calls atomic.Int32
	block <-chan struct{}
}

func (f *terrain) Summary(context.Context, string) (mapdata.Summary, error) {
	return mapdata.Summary{}, nil
}
func (f *terrain) Tile(ctx context.Context, _ string, r mapdata.Request) (mapdata.Tile, error) {
	f.calls.Add(1)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return mapdata.Tile{}, ctx.Err()
		}
	}
	return mapdata.Tile{State: mapdata.Ready, Dimension: r.Dimension, ChunkX: r.ChunkX, ChunkZ: r.ChunkZ, MaxY: r.MaxY, Cells: make([]mapdata.Cell, 256), Fingerprint: "source-v1", SampledAt: time.Now()}, nil
}
func TestViewBoundsCacheExpiryAndIsolation(t *testing.T) {
	f := &terrain{}
	s := New(f, nil)
	now := time.Now()
	s.now = func() time.Time { return now }
	r := mapdata.Request{Dimension: "minecraft:overworld"}
	v, e := s.View(context.Background(), "alpha", r)
	if e != nil || len(v.Tiles) != 9 || f.calls.Load() != 9 {
		t.Fatal(v, e, f.calls.Load())
	}
	v.Tiles[0].Cells[0].Block = "tamper"
	v, e = s.View(context.Background(), "alpha", r)
	if e != nil || f.calls.Load() != 9 || v.Tiles[0].Cells[0].Block == "tamper" {
		t.Fatal("cache must be bounded and immutable", e, f.calls.Load())
	}
	now = now.Add(5 * time.Second)
	_, e = s.View(context.Background(), "alpha", r)
	if e != nil || f.calls.Load() != 18 {
		t.Fatal("next sample must revalidate sources", e, f.calls.Load())
	}
	for i := int32(0); i < 20; i++ {
		r.ChunkX = i * 3
		if _, e = s.View(context.Background(), "alpha", r); e != nil {
			t.Fatal(e)
		}
	}
	if len(s.cache) > 32 || s.bytes > 4<<20 {
		t.Fatal("cache exceeded bounds", len(s.cache), s.bytes)
	}
	r.ChunkX = 1874999
	if _, e = s.View(context.Background(), "alpha", r); e == nil {
		t.Fatal("edge view exceeded chunk bounds")
	}
	r.ChunkX = 0
	r.Dimension = "../outside"
	if _, e = s.View(context.Background(), "alpha", r); e == nil {
		t.Fatal("unsafe dimension accepted")
	}
}
func TestAdmissionAndCancellation(t *testing.T) {
	release := make(chan struct{})
	f := &terrain{block: release}
	s := New(f, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	for range 2 {
		go func() { _, e := s.View(ctx, "alpha", mapdata.Request{Dimension: "minecraft:overworld"}); done <- e }()
	}
	deadline := time.After(time.Second)
	for f.calls.Load() != 2 {
		select {
		case <-deadline:
			t.Fatal("workers not admitted")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if _, e := s.View(context.Background(), "alpha", mapdata.Request{Dimension: "minecraft:overworld"}); e == nil {
		t.Fatal("unbounded admission")
	}
	cancel()
	for range 2 {
		if e := <-done; e == nil {
			t.Fatal("cancel lost")
		}
	}
	if len(s.admission) != 0 {
		t.Fatal("admission leaked")
	}
	close(release)
}

type presenceProbe struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (p *presenceProbe) PlayerPositions(ctx context.Context, id string) (console.MapPlayers, error) {
	p.calls.Add(1)
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return console.MapPlayers{}, ctx.Err()
		}
	}
	return console.MapPlayers{Snapshot: console.PlayerSnapshot{InstanceID: id, Known: true, Players: []console.PlayerIdentity{{Name: "Alex", Authority: "name_only"}}}, Positions: []console.MapPosition{}}, nil
}
func TestPresenceSingleflightAndFiveSecondExpiry(t *testing.T) {
	p := &presenceProbe{started: make(chan struct{}, 2), release: make(chan struct{})}
	s := New(&terrain{}, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan console.MapPlayers, 2)
	go func() { done <- s.samplePresence(ctx, "alpha") }()
	<-p.started
	go func() { done <- s.samplePresence(ctx, "alpha") }()
	time.Sleep(10 * time.Millisecond)
	close(p.release)
	for range 2 {
		if v := <-done; !v.Snapshot.Known {
			t.Fatal(v)
		}
	}
	if p.calls.Load() != 1 {
		t.Fatal("viewers multiplied native work", p.calls.Load())
	}
	v := s.samplePresence(ctx, "alpha")
	v.Snapshot.Players[0].Name = "tamper"
	if s.samplePresence(ctx, "alpha").Snapshot.Players[0].Name != "Alex" {
		t.Fatal("cache leaked mutable identity")
	}
	s.mu.Lock()
	s.presence["alpha"] = presenceEntry{expires: time.Now().Add(-time.Second)}
	s.mu.Unlock()
	if v = s.samplePresence(ctx, "alpha"); !v.Snapshot.Known || p.calls.Load() != 2 {
		t.Fatal(v, p.calls.Load())
	}
}
