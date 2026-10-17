package candidate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSigningInputNeverIncludesMaterialInErrors(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{23}, 32))
	for _, data := range [][]byte{key, key.Seed(), []byte(base64.StdEncoding.EncodeToString(key)), []byte(base64.StdEncoding.EncodeToString(key.Seed()) + "\n")} {
		got, err := ReadSigningKey(bytes.NewReader(data))
		if err != nil || !bytes.Equal(got, key) {
			t.Fatal("valid supplied key refused", err)
		}
	}
	for _, secret := range []string{"SECRET-MATERIAL-INVALID", strings.Repeat("secret", 1000)} {
		if _, err := ReadSigningKey(strings.NewReader(secret)); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("invalid key accepted or leaked")
		}
	}
	p := filepath.Join(t.TempDir(), "private.key")
	if e := os.WriteFile(p, key, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenSigningKey(p); e != nil {
		t.Fatal(e)
	}
	if runtime.GOOS != "windows" {
		if e := os.Chmod(p, 0644); e != nil {
			t.Fatal(e)
		}
		if _, e := OpenSigningKey(p); e == nil {
			t.Fatal("publicly readable key accepted")
		}
	}
}
