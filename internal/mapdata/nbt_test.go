package mapdata

import (
	"context"
	"encoding/hex"
	"os"
	"testing"
)

// Literal bytes independently encode a named root with one signed byte Y=-4.
// Dropping signedness or accepting truncated/malicious collections breaks this.
func TestNBTLiteralSignedAndBounded(t *testing.T) {
	b, _ := hex.DecodeString("0a000001000159fc00")
	n, e := parseNBT(context.Background(), b)
	if e != nil {
		t.Fatal(e)
	}
	if n["Y"].value != int8(-4) {
		t.Fatalf("signed byte: %#v", n["Y"])
	}
	for _, bad := range []string{"0a000001000159", "0a0000070001787fffffff", "0a00000100015901010001590200", "0a00000000"} {
		b, _ = hex.DecodeString(bad)
		if _, e = parseNBT(context.Background(), b); e == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	c, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = parseNBT(c, []byte{10, 0, 0, 0}); e == nil {
		t.Fatal("canceled parse succeeded")
	}
}
func TestModifiedUTF(t *testing.T) {
	// Java modified UTF: encoded NUL and surrogate pair U+1F600.
	b, _ := hex.DecodeString("c080eda0bdedb880")
	got, e := modifiedUTF(b)
	if e != nil || got != "\x00😀" {
		t.Fatalf("%q %v", got, e)
	}
	for _, bad := range [][]byte{{0}, {0xc1, 0x81}, {0xed, 0xa0, 0x80}, {0xf0, 0x9f, 0x98, 0x80}} {
		if _, e := modifiedUTF(bad); e == nil {
			t.Fatalf("invalid encoding accepted: %x", bad)
		}
	}
}

// Fuzz seeds include the actual synthetic serialized schemas; bounded parser
// failures are expected, while panics or accidental unbounded allocations are bugs.
func FuzzChunkDecoder(f *testing.F) {
	for _, name := range []string{"1.12.2-legacy", "1.13-dense", "1.16-padded", "1.18-negative", "1.20.5-modern"} {
		b, e := os.ReadFile("testdata/" + name + ".nbt")
		if e != nil {
			f.Fatal(e)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > maxDecoded {
			return
		}
		_, _ = decodeChunk(context.Background(), b, Request{})
	})
}
