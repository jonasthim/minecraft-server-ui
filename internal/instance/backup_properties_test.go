package instance

import (
	"strings"
	"testing"
)

func TestBackupPropertiesRemovesDecodedPrivateControls(t *testing.T) {
	raw := []byte("# public\nmanagement-server-secret=private\nmanagement\\u002dserver-secret=escaped\nmanagement-server-\\\n secret=continued\nrcon.password=hidden\nmax-players=20\ncustom\\:key=value\n")
	out, err := BackupProperties(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private", "escaped", "continued", "hidden"} {
		if strings.Contains(string(out), secret) {
			t.Fatalf("credential leaked: %s", out)
		}
	}
	if !strings.Contains(string(out), "max-players=20") || !strings.Contains(string(out), "custom\\:key=value") {
		t.Fatal("ordinary properties lost")
	}
}
