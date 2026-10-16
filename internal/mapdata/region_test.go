package mapdata

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func syntheticRegion(t *testing.T, codec byte, data []byte, external bool) []byte {
	t.Helper()
	n := len(data) + 1
	if external {
		n = 1
	}
	sectors := (n + 4 + 4095) / 4096
	out := make([]byte, (2+sectors)*4096)
	binary.BigEndian.PutUint32(out, (2<<8)|uint32(sectors))
	binary.BigEndian.PutUint32(out[4096:], 1700000000)
	binary.BigEndian.PutUint32(out[8192:], uint32(n))
	out[8196] = codec
	if external {
		out[8196] |= 128
	} else {
		copy(out[8197:], data)
	}
	return out
}
func TestRegionValidatesTableAndExternal(t *testing.T) {
	b, e := os.ReadFile("testdata/1.20.5-modern.nbt")
	if e != nil {
		t.Fatal(e)
	}
	base := syntheticRegion(t, 3, b, false)
	for _, tc := range []struct {
		name   string
		mutate func([]byte)
		trim   int
	}{{"valid", func([]byte) {}, 0}, {"overlap", func(b []byte) { copy(b[4:8], b[:4]) }, 0}, {"header-sector", func(b []byte) { binary.BigEndian.PutUint32(b, 257) }, 0}, {"zero-count", func(b []byte) { binary.BigEndian.PutUint32(b, 512) }, 0}, {"out-of-file", func(b []byte) { binary.BigEndian.PutUint32(b, 0x40000001) }, 0}, {"length", func(b []byte) { binary.BigEndian.PutUint32(b[8192:], 0xffffff) }, 0}, {"partial-table", func([]byte) {}, 4097}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := bytes.Clone(base)
			tc.mutate(raw)
			if tc.trim > 0 {
				raw = raw[:tc.trim]
			}
			if e := os.WriteFile(filepath.Join(dir, "r.0.0.mca"), raw, 0600); e != nil {
				t.Fatal(e)
			}
			s, e := openSource(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			r, e := readRegion(context.Background(), s, Request{})
			if tc.name == "valid" {
				if e != nil {
					t.Fatal(e)
				}
				defer r.close()
				if !bytes.Equal(r.data, b) || r.codec != 3 {
					t.Fatal("payload mismatch")
				}
			} else if e == nil {
				r.close()
				t.Fatal("bad region accepted")
			}
		})
	}
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "r.0.0.mca"), syntheticRegion(t, 3, nil, true), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "c.0.0.mcc"), b, 0600); e != nil {
		t.Fatal(e)
	}
	s, e := openSource(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	r, e := readRegion(context.Background(), s, Request{})
	if e != nil {
		t.Fatal(e)
	}
	defer r.close()
	if !bytes.Equal(r.data, b) || r.external == nil {
		t.Fatal("external mismatch")
	}
	if e := os.WriteFile(filepath.Join(dir, "c.0.0.mcc"), []byte("changed"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := r.verify(context.Background()); e == nil {
		t.Fatal("external mutation accepted")
	}
}
