package candidate

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func TestPublicTrustRequiresIndependentRepositoryAndBoundedKeys(t *testing.T) {
	body := []byte(`{"format":1,"repository":"example/repo","public_keys":{"fixture":"` + base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize)) + `"}}`)
	if _, e := ParsePublicTrust(body, "example/repo"); e != nil {
		t.Fatal(e)
	}
	for _, bad := range [][]byte{append([]byte(`{"format":1,`), body[1:]...), append([]byte(`{"unknown":1,`), body[1:]...), []byte(strings.Replace(string(body), "example/repo", "elsewhere/repo", 1)), []byte(strings.Replace(string(body), "fixture", "bad/key", 1)), []byte(strings.Replace(string(body), base64.StdEncoding.EncodeToString(make([]byte, 32)), "invalid", 1)), []byte(strings.Repeat(" ", 16385))} {
		if _, e := ParsePublicTrust(bad, "example/repo"); e == nil {
			t.Fatal("invalid public trust accepted")
		}
	}
}
