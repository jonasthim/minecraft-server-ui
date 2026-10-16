package console

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type bootstrapContext struct {
	context.Context
	calls  int
	at     int
	change func()
}

func (c *bootstrapContext) Err() error {
	c.calls++
	if c.calls == c.at {
		c.change()
	}
	return nil
}

func bootstrapSubstitute(t *testing.T, original, outside string) func() {
	t.Helper()
	link := original + ".link"
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	return func() {
		if err := os.Rename(original, original+".held"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			os.Remove(original)
			if err := os.Rename(original+".held", original); err != nil {
				t.Error(err)
			}
		})
		if err := os.Rename(link, original); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryAndCursorRejectAncestorSubstitution(t *testing.T) {
	for _, operation := range []string{"history", "cursor", "poll"} {
		t.Run(operation, func(t *testing.T) {
			h, path := logFixture(t)
			if err := os.WriteFile(path, []byte("inside\n"), 0600); err != nil {
				t.Fatal(err)
			}
			dirs, err := h.paths.For("test")
			if err != nil {
				t.Fatal(err)
			}
			data := filepath.Dir(filepath.Dir(dirs.Root))
			outside := t.TempDir()
			outLogs := filepath.Join(outside, "instances", "test", "logs")
			if err = os.MkdirAll(outLogs, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(outLogs, "console.log"), []byte("outside secret\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx := &bootstrapContext{Context: t.Context(), at: 2, change: bootstrapSubstitute(t, data, outside)}
			switch operation {
			case "history":
				err = h.Capture(ctx, "test")
			case "cursor":
				ctx.at = 1
				_, err = newRawCursor(ctx, h.paths, "test")
			case "poll":
				var cursor *rawCursor
				cursor, err = newRawCursor(t.Context(), h.paths, "test")
				if err != nil {
					t.Fatal(err)
				}
				_, err = cursor.poll(ctx)
			}
			if err == nil || domain.AsError(err).Code != domain.CodeValidationFailed {
				t.Fatal("substituted log root was not refused at bootstrap", err)
			}
			if ctx.calls < ctx.at {
				t.Fatal("substitution window not reached")
			}
			var count int
			if err = h.db.QueryRow(`SELECT count(*) FROM console_lines`).Scan(&count); err != nil || count != 0 {
				t.Fatal("outside history persisted", count, err)
			}
		})
	}
}
func TestHistoryMissingLogDirectoryRemainsEmpty(t *testing.T) {
	h, path := logFixture(t)
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := h.Capture(t.Context(), "test"); err != nil {
		t.Fatal("missing logs changed from empty to error", err)
	}
}
func TestCursorMissingRootPreservesCause(t *testing.T) {
	h, path := logFixture(t)
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if _, err := newRawCursor(t.Context(), h.paths, "test"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing cursor cause lost", err)
	}
}
