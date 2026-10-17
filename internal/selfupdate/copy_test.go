package selfupdate

import (
	"os"
	"testing"
)

// Regression for reopening a completed executable copy read-only before Sync:
// Windows FlushFileBuffers requires write access even when the bytes were
// already flushed by the copy. Permission changes must still be flushed.
func TestCopyVerifiedFlushesExecutablePermissionsAndPreservesSource(t *testing.T) {
	source, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	body := []byte("verified executable")
	if err := source.WriteFile("source", body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyVerified(t.Context(), source, "source", destination, "previous", Installed{SHA256: digest(body)}); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		root *os.Root
		name string
	}{{source, "source"}, {destination, "previous"}} {
		got, err := file.root.ReadFile(file.name)
		if err != nil || string(got) != string(body) {
			t.Fatalf("%s contents=%q error=%v", file.name, got, err)
		}
	}
	if err := copyVerified(t.Context(), source, "source", destination, "rejected", Installed{SHA256: digest([]byte("wrong"))}); err == nil {
		t.Fatal("unverified executable accepted")
	}
}
