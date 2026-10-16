//go:build linux

package catalog

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestCleanupPermissionFailureIsReported(t *testing.T) {
	var s *Service
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s = service(t, func(*http.Request) (*http.Response, error) {
		if e := os.Chmod(s.dir, 0500); e != nil {
			t.Fatal(e)
		}
		cancel()
		return response("abc"), nil
	})
	defer os.Chmod(s.dir, 0700)
	_, e := s.downloadFile(ctx, Selection{Provider: Modrinth}, File{ID: "f", Filename: "a.jar", URL: "https://cdn.modrinth.com/a.jar", Size: 3, Hashes: map[string]string{"sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}}, Compatibility{})
	if e == nil || !strings.Contains(domain.AsError(e).Message, "cleanup") {
		t.Fatalf("retained staging cleanup failure not reported: %v", e)
	}
	entries, readErr := os.ReadDir(s.dir)
	if readErr != nil || len(entries) != 1 {
		t.Fatal(entries, readErr)
	}
}
