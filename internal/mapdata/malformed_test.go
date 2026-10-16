package mapdata

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"sort"
	"testing"
)

// A tiny test-only writer permits focused corruptions of independent fixtures.
// Expected blocks and geometry never come from this writer or decoder helpers.
func encodeNBT(t *testing.T, c compound) []byte {
	t.Helper()
	var b bytes.Buffer
	b.Write([]byte{10, 0, 0})
	writeTag(t, &b, tag{10, c})
	return b.Bytes()
}
func writeString(t *testing.T, b *bytes.Buffer, s string) {
	t.Helper()
	if len(s) > 65535 {
		t.Fatal("test string too long")
	}
	_ = binary.Write(b, binary.BigEndian, uint16(len(s)))
	b.WriteString(s)
}
func writeTag(t *testing.T, b *bytes.Buffer, n tag) {
	t.Helper()
	switch n.kind {
	case 1, 2, 3, 4, 5, 6:
		_ = binary.Write(b, binary.BigEndian, n.value)
	case 7:
		v := n.value.([]byte)
		_ = binary.Write(b, binary.BigEndian, int32(len(v)))
		b.Write(v)
	case 8:
		writeString(t, b, n.value.(string))
	case 9:
		v := n.value.(list)
		b.WriteByte(v.kind)
		_ = binary.Write(b, binary.BigEndian, int32(len(v.values)))
		for _, e := range v.values {
			writeTag(t, b, e)
		}
	case 10:
		v := n.value.(compound)
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			e := v[k]
			b.WriteByte(e.kind)
			writeString(t, b, k)
			writeTag(t, b, e)
		}
		b.WriteByte(0)
	case 12:
		v := n.value.([]uint64)
		_ = binary.Write(b, binary.BigEndian, int32(len(v)))
		_ = binary.Write(b, binary.BigEndian, v)
	default:
		t.Fatalf("test writer unsupported type %d", n.kind)
	}
}
func modernFixture(t *testing.T) compound {
	t.Helper()
	b, e := os.ReadFile("testdata/1.20.5-modern.nbt")
	if e != nil {
		t.Fatal(e)
	}
	root, e := parseNBT(context.Background(), b)
	if e != nil {
		t.Fatal(e)
	}
	return root
}
func firstSection(c compound) compound { return c["sections"].value.(list).values[0].value.(compound) }
func TestRejectMalformedChunkContainers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(compound)
	}{
		{"duplicate-y", func(c compound) {
			s := c["sections"].value.(list)
			s.values = append(s.values, s.values[0])
			c["sections"] = tag{9, s}
		}},
		{"wrong-y-type", func(c compound) { firstSection(c)["Y"] = tag{3, int32(-4)} }},
		{"unbounded-y", func(c compound) { firstSection(c)["Y"] = tag{1, int8(-128)} }},
		{"short-packed-array", func(c compound) {
			s := firstSection(c)["block_states"].value.(compound)
			s["data"] = tag{12, []uint64{0}}
		}},
		{"bad-palette-index", func(c compound) {
			s := firstSection(c)["block_states"].value.(compound)
			data := s["data"].value.([]uint64)
			data[0] |= 31
		}},
		{"unsafe-palette-name", func(c compound) {
			s := firstSection(c)["block_states"].value.(compound)
			s["palette"].value.(list).values[0].value.(compound)["Name"] = tag{8, "secret\nvalue"}
		}},
		{"no-palette", func(c compound) { s := firstSection(c)["block_states"].value.(compound); delete(s, "palette") }},
		{"wrong-section-kind", func(c compound) { c["sections"] = tag{9, list{1, []tag{{1, int8(0)}}}} }},
		{"too-many-sections", func(c compound) {
			s := c["sections"].value.(list)
			s.values = make([]tag, 65)
			for i := range s.values {
				s.values[i] = tag{10, compound{"Y": {1, int8(i)}}}
			}
			c["sections"] = tag{9, s}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := modernFixture(t)
			tc.mutate(c)
			_, e := decodeChunk(context.Background(), encodeNBT(t, c), Request{})
			if !errors.Is(e, errCorrupt) {
				t.Fatalf("wanted corrupt, got %v", e)
			}
		})
	}
}
func TestModernSinglePaletteAndLightOnlySections(t *testing.T) {
	c := modernFixture(t)
	s := firstSection(c)["block_states"].value.(compound)
	s["palette"] = tag{9, list{10, []tag{{10, compound{"Name": {8, "example:single"}}}}}}
	delete(s, "data")
	sections := c["sections"].value.(list)
	sections.values = append(sections.values, tag{10, compound{"Y": {1, int8(-5)}, "SkyLight": {7, make([]byte, 2048)}}})
	c["sections"] = tag{9, sections}
	out, e := decodeChunk(context.Background(), encodeNBT(t, c), Request{})
	if e != nil {
		t.Fatal(e)
	}
	if out.Cells[0].Y != -49 || out.Cells[0].Block != "example:single" || out.Cells[0].Known || out.Cells[0].Empty {
		t.Fatalf("single/light: %+v", out.Cells[0])
	}
}
func TestNBTDepthAndCollectionBombs(t *testing.T) {
	c := compound{}
	for i := 0; i < 40; i++ {
		c = compound{"nested": {10, c}}
	}
	if _, e := parseNBT(context.Background(), encodeNBT(t, c)); e == nil {
		t.Fatal("deep NBT accepted")
	}
	b := append([]byte{10, 0, 0, 9, 0, 1, 'x', 1}, []byte{0x7f, 0xff, 0xff, 0xff}...)
	if _, e := parseNBT(context.Background(), b); e == nil {
		t.Fatal("huge list accepted")
	}
}
func TestDecompressionBoundsAndInvalidCodecs(t *testing.T) {
	bomb := compressed(t, 2, make([]byte, maxDecoded+1))
	if _, e := decompress(context.Background(), 2, bomb); e == nil {
		t.Fatal("decompression bomb accepted")
	}
	if _, e := decompress(context.Background(), 2, append(compressed(t, 2, []byte{10, 0, 0, 0}), 99)); e == nil {
		t.Fatal("trailing compressed bytes accepted")
	}
	if _, e := decompress(context.Background(), 127, nil); !errors.Is(e, errUnsupported) {
		t.Fatalf("custom codec %v", e)
	}
}

func TestLegacyUnresolvedMetadataKeepsNumericIdentity(t *testing.T) {
	b, e := os.ReadFile("testdata/1.12.2-legacy.nbt")
	if e != nil {
		t.Fatal(e)
	}
	c, e := parseNBT(context.Background(), b)
	if e != nil {
		t.Fatal(e)
	}
	section := c["Level"].value.(compound)["Sections"].value.(list).values[0].value.(compound)
	// Stone metadata1 is not plain stone. A partial original palette must keep
	// unresolved variants labelled instead of silently replacing their identity.
	section["Data"].value.([]byte)[0] = 1
	y := int32(0)
	out, e := decodeChunk(context.Background(), encodeNBT(t, c), Request{MaxY: &y})
	if e != nil {
		t.Fatal(e)
	}
	if out.Cells[0].Block != "legacy:1:1" || out.Cells[0].Known || out.Cells[0].Empty {
		t.Fatalf("variant misidentified: %+v", out.Cells[0])
	}
}
