package mapdata

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPinnedSourceDetectsSameTimestampMutation(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "data")
	if e := os.WriteFile(name, []byte("before"), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := openSource(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	f, e := s.open("data", 100)
	if e != nil {
		t.Fatal(e)
	}
	defer f.f.Close()
	b, e := f.readAt(context.Background(), 0, 6)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(name, []byte("after!"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Chtimes(name, time.Now(), f.info.ModTime()); e != nil {
		t.Fatal(e)
	}
	if e = f.verify(context.Background(), []span{{offset: 0, data: b}}); e == nil {
		t.Fatal("same size/timestamp changed bytes accepted")
	}
}
func TestPinnedSourceRejectsLinksAndDirectoryReplacement(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if e := os.WriteFile(filepath.Join(outside, "data"), []byte("secret"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(dir, "link")); e != nil {
		t.Skip(e)
	}
	s, e := openSource(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.descend("link"); e == nil {
		t.Fatal("directory symlink accepted")
	}
	if e = os.Mkdir(filepath.Join(dir, "region"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = s.descend("region"); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(filepath.Join(dir, "region"), filepath.Join(dir, "old")); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(filepath.Join(dir, "region"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = s.verify(); e == nil {
		t.Fatal("replaced directory accepted")
	}
}
