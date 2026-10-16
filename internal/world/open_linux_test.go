//go:build linux

package world

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestHardlinkAndFIFORefused(t *testing.T) {
	for _, kind := range []string{"hardlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			s, base := exportFixture(t)
			target := filepath.Join(base, "My world", "unsafe.dat")
			var e error
			if kind == "hardlink" {
				e = os.Link(filepath.Join(base, "server.properties"), target)
			} else {
				e = syscall.Mkfifo(target, 0600)
			}
			if e != nil {
				t.Skip(e)
			}
			a, e := s.Capture(context.Background(), operator, "alpha")
			if a != nil {
				a.Close()
			}
			if e == nil {
				t.Fatal("unsafe source captured")
			}
		})
	}
}
