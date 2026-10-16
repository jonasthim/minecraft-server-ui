package mapdata

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type configStore struct{ version string }

func (s configStore) Get(context.Context, string) (instance.Detail, error) {
	return instance.Detail{Instance: domain.Instance{ID: "alpha", MinecraftVersion: s.version, Flavor: domain.FlavorVanilla}, Config: domain.InstanceConfig{LevelName: "Test World"}}, nil
}
func putBytes(t *testing.T, base, name string, b []byte) {
	t.Helper()
	p := filepath.Join(base, filepath.FromSlash(name))
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func compressed(t *testing.T, codec byte, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	switch codec {
	case 1:
		w := gzip.NewWriter(&out)
		if _, e := w.Write(b); e != nil {
			t.Fatal(e)
		}
		if e := w.Close(); e != nil {
			t.Fatal(e)
		}
	case 2:
		w := zlib.NewWriter(&out)
		if _, e := w.Write(b); e != nil {
			t.Fatal(e)
		}
		if e := w.Close(); e != nil {
			t.Fatal(e)
		}
	case 3:
		return b
	case 4:
		b, e := os.ReadFile("testdata/lz4-oracle.bin")
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	return out.Bytes()
}
func mapFixture(t *testing.T, version, name, dimension string, codec byte) (*Service, string) {
	t.Helper()
	p, e := instance.NewPaths(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	d, e := p.For("alpha")
	if e != nil {
		t.Fatal(e)
	}
	level, e := os.ReadFile("testdata/" + name + "-level.nbt")
	if e != nil {
		t.Fatal(e)
	}
	putBytes(t, d.Server, "Test World/level.dat", compressed(t, 1, level))
	b, e := os.ReadFile("testdata/" + name + ".nbt")
	if e != nil {
		t.Fatal(e)
	}
	putBytes(t, d.Server, dimension+"/region/r.0.0.mca", syntheticRegion(t, codec, compressed(t, codec, b), false))
	return New(world.New(nil, configStore{version}, p, nil), p), d.Server
}
func TestServiceConsumesReviewedDiscoveryAcrossLayoutsAndCodecs(t *testing.T) {
	for _, tc := range []struct {
		version, name, path, dim string
		codec                    byte
	}{{"1.12.2", "1.12.2-legacy", "Test World", "minecraft:overworld", 1}, {"1.16.1", "1.16-padded", "Test World", "minecraft:overworld", 2}, {"1.18.1", "1.18-negative", "Test World", "minecraft:overworld", 3}, {"1.20.5", "1.20.5-modern", "Test World/dimensions/example/moon", "example:moon", 4}, {"1.21.9", "1.21.9-modern", "Test World", "minecraft:overworld", 2}, {"26.1", "26.1-modern", "Test World/dimensions/minecraft/overworld", "minecraft:overworld", 2}, {"26.3", "26.3-modern", "Test World/dimensions/minecraft/overworld", "minecraft:overworld", 3}} {
		t.Run(tc.version, func(t *testing.T) {
			s, _ := mapFixture(t, tc.version, tc.name, tc.path, tc.codec)
			summary, e := s.Summary(context.Background(), "alpha")
			if e != nil || summary.State != Ready || summary.DataVersion < 1343 {
				t.Fatalf("summary %+v %v", summary, e)
			}
			tile, e := s.Tile(context.Background(), "alpha", Request{Dimension: tc.dim})
			if e != nil || tile.State != Ready || len(tile.Cells) != 256 || tile.Fingerprint == "" || tile.SourceMTime.IsZero() {
				t.Fatalf("tile %+v %v", tile, e)
			}
			again, e := s.Tile(context.Background(), "alpha", Request{Dimension: tc.dim})
			if e != nil || again.Fingerprint != tile.Fingerprint {
				t.Fatalf("unstable fingerprint %v", e)
			}
			data, _ := json.Marshal(summary)
			if strings.Contains(string(data), "PrivateTestSecret") {
				t.Fatal("private NBT leaked")
			}
		})
	}
}
func TestServiceMissingValidationCancellationAndMutation(t *testing.T) {
	s, base := mapFixture(t, "1.20.5", "1.20.5-modern", "Test World", 2)
	for _, req := range []Request{{Dimension: "../secret"}, {Dimension: "minecraft:overworld", ChunkX: 1875000}, {Dimension: "minecraft:overworld", MaxY: func() *int32 { v := int32(-2033); return &v }()}} {
		if _, e := s.Tile(context.Background(), "alpha", req); e == nil || domain.AsError(e).Code != domain.CodeValidationFailed {
			t.Fatalf("bad input accepted: %+v %v", req, e)
		}
	}
	c, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Tile(c, "alpha", Request{Dimension: "minecraft:overworld"}); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancellation %v", e)
	}
	missing, e := s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:the_end"})
	if e != nil || missing.State != Missing || len(missing.Cells) > 0 {
		t.Fatalf("missing %+v %v", missing, e)
	}
	old, e := s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:overworld"})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile("testdata/1.20.5-modern.nbt")
	b = bytes.Replace(b, []byte("minecraft:stone"), []byte("minecraft:sands"), 1)
	putBytes(t, base, "Test World/region/r.0.0.mca", syntheticRegion(t, 3, b, false))
	next, e := s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:overworld"})
	if e != nil || next.State != Ready || next.Fingerprint == old.Fingerprint {
		t.Fatalf("changed source fingerprint %+v %v", next, e)
	}
}

func TestServiceExposesFailureStatesWithoutFabricatedCells(t *testing.T) {
	s, base := mapFixture(t, "1.20.5", "1.20.5-modern", "Test World", 3)
	path := filepath.Join(base, "Test World", "region", "r.0.0.mca")
	for _, tc := range []struct {
		name string
		raw  []byte
		want State
	}{{"missing", make([]byte, 8192), Missing}, {"unsupported", syntheticRegion(t, 127, []byte{0}, false), Unsupported}, {"corrupt", []byte{0}, Corrupt}, {"external-missing", syntheticRegion(t, 3, nil, true), Stale}} {
		t.Run(tc.name, func(t *testing.T) {
			if e := os.WriteFile(path, tc.raw, 0600); e != nil {
				t.Fatal(e)
			}
			tile, e := s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:overworld"})
			if e != nil || tile.State != tc.want || len(tile.Cells) != 0 || tile.Fingerprint != "" {
				t.Fatalf("%+v %v", tile, e)
			}
		})
	}
	putBytes(t, base, "Test World/DIM7/region/r.0.0.mca", make([]byte, 8192))
	unknown, e := s.Tile(context.Background(), "alpha", Request{Dimension: "unknown:DIM7"})
	if e != nil || unknown.State != Unknown || len(unknown.Cells) > 0 {
		t.Fatalf("unknown %+v %v", unknown, e)
	}
}
func TestServiceNoPrivateNBTOrConfigLeak(t *testing.T) {
	s, base := mapFixture(t, "1.20.5", "1.20.5-modern", "Test World", 3)
	b, e := os.ReadFile("testdata/level.nbt")
	if e != nil {
		t.Fatal(e)
	}
	putBytes(t, base, "Test World/level.dat", compressed(t, 1, b))
	out, e := s.Summary(context.Background(), "alpha")
	if e != nil || out.State != Ready {
		t.Fatal(out, e)
	}
	data, e := json.Marshal(out)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "PrivateTestSecret") || strings.Contains(string(data), "123456") || strings.Contains(string(data), base) {
		t.Fatalf("private data leaked: %s", data)
	}
}

type blockingDiscovery struct {
	next    Discoverer
	started chan struct{}
	release chan struct{}
}

func (b blockingDiscovery) Discover(ctx context.Context, id string) (world.Discovery, error) {
	b.started <- struct{}{}
	select {
	case <-b.release:
		return b.next.Discover(ctx, id)
	case <-ctx.Done():
		return world.Discovery{}, ctx.Err()
	}
}
func TestServiceBoundsConcurrentWorkAndReleasesAdmission(t *testing.T) {
	s, _ := mapFixture(t, "1.20.5", "1.20.5-modern", "Test World", 3)
	b := blockingDiscovery{s.discoverer, make(chan struct{}, 2), make(chan struct{})}
	s.discoverer = b
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, e := s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:overworld"})
			results <- e
		}()
	}
	<-b.started
	<-b.started
	if _, e := s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:overworld"}); e == nil || domain.AsError(e).Code != domain.CodeUnavailable {
		t.Fatalf("concurrency limit %v", e)
	}
	close(b.release)
	for i := 0; i < 2; i++ {
		if e := <-results; e != nil {
			t.Fatal(e)
		}
	}
	s.discoverer = b.next
	if tile, e := s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:overworld"}); e != nil || tile.State != Ready {
		t.Fatalf("admission leaked %+v %v", tile, e)
	}
}
func TestServiceMixedCodecsAndDataVersionsArePerChunk(t *testing.T) {
	s, base := mapFixture(t, "1.20.5", "1.20.5-modern", "Test World", 2)
	old, e := os.ReadFile("testdata/1.12.2-legacy.nbt")
	if e != nil {
		t.Fatal(e)
	}
	root, e := parseNBT(context.Background(), old)
	if e != nil {
		t.Fatal(e)
	}
	root["Level"].value.(compound)["xPos"] = tag{3, int32(1)}
	old = encodeNBT(t, root)
	p := filepath.Join(base, "Test World", "region", "r.0.0.mca")
	raw, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	second := syntheticRegion(t, 1, compressed(t, 1, old), false)
	offset := len(raw) / 4096
	raw = append(raw, second[8192:]...)
	binary.BigEndian.PutUint32(raw[4:], uint32(offset<<8)|1)
	if e := os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
	modern, e := s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:overworld"})
	if e != nil || modern.DataVersion != 3837 || modern.Codec != "zlib" {
		t.Fatalf("modern %+v %v", modern, e)
	}
	legacy, e := s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:overworld", ChunkX: 1})
	if e != nil || legacy.DataVersion != 1343 || legacy.Format != "legacy" || legacy.Codec != "gzip" || legacy.Cells[0].Y != 5 {
		t.Fatalf("legacy %+v %v", legacy, e)
	}
}

func TestNegativeChunkCoordinatesAndExternalLinkRejection(t *testing.T) {
	s, base := mapFixture(t, "1.20.5", "1.20.5-modern", "Test World", 3)
	root := modernFixture(t)
	root["xPos"] = tag{3, int32(-1)}
	b := encodeNBT(t, root)
	raw := syntheticRegion(t, 3, b, false)
	copy(raw[31*4:31*4+4], raw[:4])
	clear(raw[:4])
	putBytes(t, base, "Test World/region/r.-1.0.mca", raw)
	tile, e := s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:overworld", ChunkX: -1})
	if e != nil || tile.State != Ready || tile.Cells[12].Block != "example:boundary" {
		t.Fatalf("negative chunk %+v %v", tile, e)
	}
	outside := filepath.Join(t.TempDir(), "source")
	if e = os.WriteFile(outside, b, 0600); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			external := filepath.Join(base, "Test World", "region", "c.0.0.mcc")
			_ = os.Remove(external)
			var e error
			if kind == "symlink" {
				e = os.Symlink(outside, external)
			} else {
				e = os.Link(outside, external)
			}
			if e != nil {
				t.Skip(e)
			}
			defer os.Remove(external)
			putBytes(t, base, "Test World/region/r.0.0.mca", syntheticRegion(t, 3, nil, true))
			out, e := s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:overworld"})
			if e == nil && out.State == Ready {
				t.Fatal("linked external source accepted")
			}
			if len(out.Cells) > 0 {
				t.Fatal("linked source exposed cells")
			}
		})
	}
}

func TestTileSeparatesHeaderSaveTimeFromObservationAndFileTime(t *testing.T) {
	s, base := mapFixture(t, "1.20.5", "1.20.5-modern", "Test World", 3)
	tile, e := s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:overworld"})
	if e != nil {
		t.Fatal(e)
	}
	if tile.ChunkSavedAt == nil || tile.ChunkSavedAt.Unix() != 1700000000 || tile.SampledAt.Equal(*tile.ChunkSavedAt) {
		t.Fatalf("selected timestamp %+v", tile)
	}
	p := filepath.Join(base, "Test World", "region", "r.0.0.mca")
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	clear(b[4096:4100])
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	tile, e = s.Tile(context.Background(), "alpha", Request{Dimension: "minecraft:overworld"})
	if e != nil || tile.ChunkSavedAt != nil {
		t.Fatalf("unknown save timestamp fabricated %+v %v", tile, e)
	}
}
func TestIncompleteGenerationRemainsUnsupported(t *testing.T) {
	c := modernFixture(t)
	c["Status"] = tag{8, "minecraft:noise"}
	if _, e := decodeChunk(context.Background(), encodeNBT(t, c), Request{}); !errors.Is(e, errUnsupported) {
		t.Fatalf("incomplete generation: %v", e)
	}
}
