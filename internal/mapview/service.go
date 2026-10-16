// Package mapview bounds saved-map presentation work and short-lived fanout.
package mapview

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/mapdata"
	"strings"
	"sync"
	"time"
)

const SampleInterval = 5 * time.Second
const maxEntries = 32
const maxBytes = 3 << 20

// Terrain is the frozen, read-only decoder boundary.
type Terrain interface {
	Summary(context.Context, string) (mapdata.Summary, error)
	Tile(context.Context, string, mapdata.Request) (mapdata.Tile, error)
}
type Presence interface {
	PlayerPositions(context.Context, string) (console.MapPlayers, error)
}
type presenceEntry struct {
	value   console.MapPlayers
	expires time.Time
	bytes   int
}
type flight struct {
	done  chan struct{}
	value console.MapPlayers
}
type View struct {
	Tiles                 []mapdata.Tile     `json:"tiles"`
	SampledAt             time.Time          `json:"sampled_at"`
	SampleIntervalSeconds int                `json:"sample_interval_seconds"`
	Native                console.MapPlayers `json:"native"`
}
type entry struct {
	tile    mapdata.Tile
	expires time.Time
	bytes   int
	used    uint64
}
type Service struct {
	terrain       Terrain
	native        Presence
	presence      map[string]presenceEntry
	flights       map[string]*flight
	admission     chan struct{}
	mu            sync.Mutex
	cache         map[string]entry
	bytes         int
	presenceBytes int
	tick          uint64
	now           func() time.Time
}

func New(t Terrain, native Presence) *Service {
	return &Service{terrain: t, native: native, presence: map[string]presenceEntry{}, flights: map[string]*flight{}, admission: make(chan struct{}, 2), cache: map[string]entry{}, now: time.Now}
}
func (s *Service) enter(ctx context.Context) (context.Context, func(), error) {
	if e := ctx.Err(); e != nil {
		return ctx, nil, e
	}
	select {
	case s.admission <- struct{}{}:
	default:
		return ctx, nil, domain.E(domain.CodeUnavailable, "Map view capacity reached; retry in five seconds.")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	return ctx, func() { cancel(); <-s.admission }, nil
}
func (s *Service) Summary(ctx context.Context, id string) (mapdata.Summary, error) {
	ctx, leave, e := s.enter(ctx)
	if e != nil {
		return mapdata.Summary{}, e
	}
	defer leave()
	return s.terrain.Summary(ctx, id)
}
func validate(id string, r mapdata.Request) error {
	if domain.ValidateInstanceID(id) != nil || r.Dimension == "" || len(r.Dimension) > 1024 || strings.ContainsAny(r.Dimension, "\\\n\r\x00") || strings.Contains(r.Dimension, "..") || r.ChunkX < -1874999 || r.ChunkX > 1874998 || r.ChunkZ < -1874999 || r.ChunkZ > 1874998 || r.MaxY != nil && (*r.MaxY < -2032 || *r.MaxY > 2031) {
		return domain.E(domain.CodeValidationFailed, "Invalid map instance, dimension, view center or maximum height.")
	}
	return nil
}

// View reads a fixed 3x3 area; every expired tile passes through the decoder's
// identity/content rechecks. Requests retain no subscriptions or background work.
func (s *Service) View(ctx context.Context, id string, r mapdata.Request) (View, error) {
	out := View{Tiles: []mapdata.Tile{}, SampledAt: s.now().UTC(), SampleIntervalSeconds: 5}
	if e := validate(id, r); e != nil {
		return out, e
	}
	ctx, leave, e := s.enter(ctx)
	if e != nil {
		return out, e
	}
	defer leave()
	out.Native = s.samplePresence(ctx, id)
	for z := int32(-1); z <= 1; z++ {
		for x := int32(-1); x <= 1; x++ {
			if e = ctx.Err(); e != nil {
				return View{}, e
			}
			req := r
			req.ChunkX += x
			req.ChunkZ += z
			y := "surface"
			if r.MaxY != nil {
				y = fmt.Sprint(*r.MaxY)
			}
			key := fmt.Sprintf("%s|%s|%d|%d|%s|mapview-v1", id, r.Dimension, req.ChunkX, req.ChunkZ, y)
			tile, ok := s.get(key)
			if !ok {
				tile, e = s.terrain.Tile(ctx, id, req)
				if e != nil {
					return View{}, e
				}
				if e = ctx.Err(); e != nil {
					return View{}, e
				}
				s.put(key, tile)
			}
			out.Tiles = append(out.Tiles, tile)
		}
	}
	return out, nil
}
func clone(t mapdata.Tile) mapdata.Tile {
	t.Cells = append([]mapdata.Cell(nil), t.Cells...)
	if t.MaxY != nil {
		y := *t.MaxY
		t.MaxY = &y
	}
	if t.ChunkSavedAt != nil {
		v := *t.ChunkSavedAt
		t.ChunkSavedAt = &v
	}
	return t
}
func (s *Service) get(key string) (mapdata.Tile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[key]
	if !ok {
		return mapdata.Tile{}, false
	}
	if !s.now().Before(e.expires) {
		delete(s.cache, key)
		s.bytes -= e.bytes
		return mapdata.Tile{}, false
	}
	s.tick++
	e.used = s.tick
	s.cache[key] = e
	return clone(e.tile), true
}
func (s *Service) put(key string, t mapdata.Tile) {
	// Only successful, fingerprinted observations are shared. Stale/error states
	// must be retried, never promoted to a valid source observation.
	if t.State != mapdata.Ready || t.Fingerprint == "" {
		return
	}
	b, e := json.Marshal(t)
	if e != nil || len(b)+len(key) > maxBytes {
		return
	}
	size := len(b) + len(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.cache[key]; ok {
		s.bytes -= old.bytes
		delete(s.cache, key)
	}
	for len(s.cache) >= maxEntries || s.bytes+size > maxBytes {
		oldest := ""
		var tick uint64 = ^uint64(0)
		for k, e := range s.cache {
			if e.used < tick {
				oldest = k
				tick = e.used
			}
		}
		if oldest == "" {
			break
		}
		s.bytes -= s.cache[oldest].bytes
		delete(s.cache, oldest)
	}
	s.tick++
	s.cache[key] = entry{clone(t), s.now().Add(SampleInterval), size, s.tick}
	s.bytes += size
}

func clonePresence(v console.MapPlayers) console.MapPlayers {
	v.Snapshot.Players = append([]console.PlayerIdentity{}, v.Snapshot.Players...)
	v.Positions = append([]console.MapPosition{}, v.Positions...)
	return v
}
func unknownPresence(id string) console.MapPlayers {
	reason := "Fresh native roster/positions are busy or unavailable. Current presence is unknown; no zero-player or coordinate assumption is made."
	return console.MapPlayers{Snapshot: console.PlayerSnapshot{InstanceID: id, Players: []console.PlayerIdentity{}, Gap: true, Reason: reason}, Positions: []console.MapPosition{}, Gap: true, Reason: reason}
}
func (s *Service) samplePresence(ctx context.Context, id string) console.MapPlayers {
	s.mu.Lock()
	if v, ok := s.presence[id]; ok && s.now().Before(v.expires) {
		s.mu.Unlock()
		return clonePresence(v.value)
	}
	if f, ok := s.flights[id]; ok {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return unknownPresence(id)
		case <-f.done:
			return clonePresence(f.value)
		}
	}
	started := s.now()
	f := &flight{done: make(chan struct{})}
	s.flights[id] = f
	s.mu.Unlock()
	value := unknownPresence(id)
	if s.native != nil {
		if v, e := s.native.PlayerPositions(ctx, id); e == nil {
			value = v
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, _ := json.Marshal(value)
	size := len(raw) + len(id)
	if old, ok := s.presence[id]; ok {
		s.presenceBytes -= old.bytes
		delete(s.presence, id)
	}
	for len(s.presence) >= 32 || s.presenceBytes+size > 1<<20 {
		oldest := ""
		var expires time.Time
		for k, v := range s.presence {
			if oldest == "" || v.expires.Before(expires) {
				oldest = k
				expires = v.expires
			}
		}
		if oldest == "" {
			break
		}
		s.presenceBytes -= s.presence[oldest].bytes
		delete(s.presence, oldest)
	}
	if size <= 1<<20 {
		s.presence[id] = presenceEntry{clonePresence(value), started.Add(SampleInterval), size}
		s.presenceBytes += size
	}
	f.value = clonePresence(value)
	delete(s.flights, id)
	close(f.done)
	return value
}
