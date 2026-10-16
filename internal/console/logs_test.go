package console

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

func logFixture(t *testing.T) (*History, string) {
	t.Helper()
	database, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	_, err = database.ExecContext(t.Context(), `INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port,created_at,updated_at) VALUES('test','Test','vanilla','1.21.9',21,1024,25565,'2026-10-16','2026-10-16')`)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := instance.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := paths.For("test")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(p.Logs, 0700); err != nil {
		t.Fatal(err)
	}
	h := NewHistory(database, paths)
	return h, filepath.Join(p.Logs, "console.log")
}
func TestHistorySplitUnicodeRedactionRestartAndRotation(t *testing.T) {
	h, path := logFixture(t)
	ctx := t.Context()
	secret := strings.Repeat("S", 40)
	h.SetSecrets("test", []string{secret})
	if err := os.WriteFile(path, []byte("hello \xe4\xb8"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.Capture(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	page, err := h.Read(ctx, "test", 0, 100)
	if err != nil || len(page.Lines) != 0 {
		t.Fatalf("partial exposed: %+v %v", page, err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.WriteString("\x96界 token=" + secret + "\nAuthorization: Bearer abc.xyz\n")
	f.Close()
	if err := h.Capture(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	page, err = h.Read(ctx, "test", 0, 100)
	if err != nil || len(page.Lines) != 2 {
		t.Fatalf("%+v %v", page, err)
	}
	if !strings.Contains(page.Lines[0].Message, "世界") || strings.Contains(page.Lines[0].Message, secret) || strings.Contains(page.Lines[1].Message, "abc.xyz") {
		t.Fatalf("redaction/unicode %+v", page)
	}
	// A new manager uses a durable byte checkpoint, never replaying persisted lines.
	resumed := NewHistory(h.db, h.paths)
	resumed.SetSecrets("test", []string{secret})
	if err := resumed.Capture(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("next\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Capture(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	page, err = resumed.Read(ctx, "test", page.NextCursor, 100)
	if err != nil || len(page.Lines) != 1 || page.Lines[0].Message != "next" {
		t.Fatalf("rotation: %+v %v", page, err)
	}
}
func TestHistoryBoundsGapAndUnterminatedSecrets(t *testing.T) {
	h, path := logFixture(t)
	ctx := t.Context()
	h.maxLines = 3
	data := "password=" + strings.Repeat("x", 20000) + "\nlast\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.Capture(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	page, err := h.Read(ctx, "test", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 2 || strings.Contains(page.Lines[0].Message, "xxx") || !page.Lines[0].Gap {
		t.Fatalf("oversized: %+v", page)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.WriteString("3\n4\n5\n")
	f.Close()
	if err := h.Capture(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	page, err = h.Read(ctx, "test", 1, 100)
	if err != nil || len(page.Lines) != 3 || !page.Gap {
		t.Fatalf("retention: %+v %v", page, err)
	}
	for _, line := range page.Lines {
		if !utf8.ValidString(line.Message) {
			t.Fatal("invalid UTF-8")
		}
	}
	if err := os.WriteFile(path, []byte("replacement\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.Capture(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	page, _ = h.Read(ctx, "test", page.NextCursor, 100)
	if len(page.Lines) != 1 || !page.Lines[0].Gap {
		t.Fatalf("truncation not disclosed: %+v", page)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := h.Capture(canceled, "test"); err == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestHistoryRejectsSymlinkAndInvalidCursor(t *testing.T) {
	h, path := logFixture(t)
	target := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(target, []byte("secret\n"), 0600)
	_ = os.Symlink(target, path)
	if err := h.Capture(t.Context(), "test"); err == nil {
		t.Fatal("followed symlink")
	}
	if _, err := h.Read(t.Context(), "../test", 0, 100); err == nil {
		t.Fatal("accepted traversal")
	}
	if _, err := h.Read(t.Context(), "test", -1, 100); err == nil {
		t.Fatal("accepted negative cursor")
	}
}
func TestRedactorCredentials(t *testing.T) {
	for _, line := range []string{`password="space secret"`, `management-server-secret=hidden`, `{"token":"hidden"}`, `https://user:pass@example.org/?key=hidden`, `Authorization: Bearer hidden`, `Cookie: session=hidden; csrf=hidden`} {
		out := Redact(line, nil)
		if strings.Contains(out, "hidden") || strings.Contains(out, "space secret") || strings.Contains(out, "user:pass") {
			t.Fatalf("leaked %q => %q", line, out)
		}
	}
}

func TestFutureHistoryCursorReplaysWithGap(t *testing.T) {
	h, path := logFixture(t)
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.Capture(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	page, err := h.Read(t.Context(), "test", 1<<50, 500)
	if err != nil || !page.Gap || len(page.Lines) != 2 || page.NextCursor != page.Lines[1].ID {
		t.Fatalf("future replay %+v %v", page, err)
	}
	page, err = h.Read(t.Context(), "test", page.NextCursor, 500)
	if err != nil || page.Gap || len(page.Lines) != 0 {
		t.Fatalf("replay repeated %+v %v", page, err)
	}
}

func TestCrossRotationPartialSecretStaysDiscardedAcrossCaptureAndRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_manager", true: "manager_restart"}[restart], func(t *testing.T) {
			h, path := logFixture(t)
			if err := os.WriteFile(path+".1", []byte("password=alpha"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("bravo"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := h.Capture(t.Context(), "test"); err != nil {
				t.Fatal(err)
			}
			if restart {
				// A newly constructed service must restore the discard state from SQL.
				h = New(h.db, h.paths, nil, nil, nil).history
			}
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString("charlie\nvisible complete line\n")
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err := h.Capture(t.Context(), "test"); err != nil {
				t.Fatal(err)
			}
			page, err := h.Read(t.Context(), "test", 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			visible := false
			for _, line := range page.Lines {
				if strings.Contains(line.Message, "alpha") || strings.Contains(line.Message, "bravo") || strings.Contains(line.Message, "charlie") {
					t.Fatalf("split secret leaked: %q", line.Message)
				}
				visible = visible || line.Message == "visible complete line"
			}
			if !visible {
				t.Fatalf("subsequent complete output lost: %+v", page)
			}
		})
	}
}

func TestDiscardedSecretContinuationAcrossMultipleRotations(t *testing.T) {
	h, path := logFixture(t)
	if err := os.WriteFile(path+".1", []byte("password=alpha"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bravo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.Capture(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".1", path+".2"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("charlie"), 0600); err != nil {
		t.Fatal(err)
	}
	h = New(h.db, h.paths, nil, nil, nil).history
	if err := h.Capture(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".2"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".1", path+".2"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("delta\nnext safe line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h = New(h.db, h.paths, nil, nil, nil).history
	if err := h.Capture(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	page, err := h.Read(t.Context(), "test", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 2 || !page.Lines[0].Gap || page.Lines[1].Message != "next safe line" {
		t.Fatalf("unsafe or missing rotation output: %+v", page)
	}
}
