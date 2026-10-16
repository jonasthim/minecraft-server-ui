package mapdata

import (
	"context"
	"os"
	"testing"
)

func TestIndependentTerrainFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, format string
		base         int32
	}{{"1.12.2-legacy", "legacy", 0}, {"1.13-dense", "palette_dense", 0}, {"1.15-dense", "palette_dense", 0}, {"1.16-padded", "palette_padded", 0}, {"1.18-negative", "modern", -64}, {"1.20.5-modern", "modern", -64}} {
		t.Run(tc.name, func(t *testing.T) {
			b, e := os.ReadFile("testdata/" + tc.name + ".nbt")
			if e != nil {
				t.Fatal(e)
			}
			out, e := decodeChunk(context.Background(), b, Request{})
			if e != nil {
				t.Fatal(e)
			}
			if out.Format != tc.format || len(out.Cells) != 256 {
				t.Fatalf("metadata: %+v", out)
			}
			for _, want := range []struct {
				i     int
				y     int32
				block string
			}{{0, tc.base + 5, "minecraft:grass_block"}, {255, tc.base + 15, "minecraft:water"}} {
				c := out.Cells[want.i]
				if c.Y != want.y || c.Block != want.block || !c.Known || c.Empty {
					t.Fatalf("cell %d: %+v want %+v", want.i, c, want)
				}
			}
			if c := out.Cells[1]; !c.Empty {
				t.Fatalf("air is not empty: %+v", c)
			}
			wantBoundary, wantAdjacent := "example:boundary", "example:ore"
			if tc.format == "legacy" {
				wantBoundary, wantAdjacent = "legacy:769:7", "legacy:770:5"
			}
			for _, want := range []struct {
				index int
				block string
			}{{12, wantBoundary}, {13, wantAdjacent}} {
				if c := out.Cells[want.index]; c.Known || c.Empty || c.Y != tc.base || c.Block != want.block {
					t.Fatalf("cell%d unknown/add/boundary: %+v want %s at Y%d", want.index, c, want.block, tc.base)
				}
			}
			y := tc.base + 4
			out, e = decodeChunk(context.Background(), b, Request{MaxY: &y})
			if e != nil || out.Cells[0].Block != "minecraft:stone" || out.Cells[0].Y != tc.base {
				t.Fatalf("slice: %+v %v", out.Cells[0], e)
			}
		})
	}
}
func TestRejectUnrecognizedVersionAndCoordinates(t *testing.T) {
	b, _ := os.ReadFile("testdata/future-schema.nbt")
	if _, e := decodeChunk(context.Background(), b, Request{}); e == nil {
		t.Fatal("accepted unrecognized future data version")
	}
	b, _ = os.ReadFile("testdata/1.18-negative.nbt")
	if _, e := decodeChunk(context.Background(), b, Request{ChunkX: 1}); e == nil {
		t.Fatal("accepted chunk at wrong position")
	}
}
