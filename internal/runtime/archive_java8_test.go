package runtime

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Official Temurin 8u504-b01 has man/ja -> ja_JP.UTF-8, a directory
// alias. Rejecting every non-file link target blocks an otherwise verified JVM.
func TestJava8ManDirectoryAlias(t *testing.T) {
	data := archiveFixture(t, "tar.gz", []fixtureEntry{
		{name: "jdk8u504-b01/", kind: tar.TypeDir},
		{name: "jdk8u504-b01/man/", kind: tar.TypeDir},
		{name: "jdk8u504-b01/man/ja_JP.UTF-8/", kind: tar.TypeDir},
		{name: "jdk8u504-b01/man/ja_JP.UTF-8/man1/", kind: tar.TypeDir},
		{name: "jdk8u504-b01/man/ja_JP.UTF-8/man1/java.1", data: "Japanese Java manual"},
		{name: "jdk8u504-b01/man/ja", kind: tar.TypeSymlink, link: "ja_JP.UTF-8"},
	})
	archive := filepath.Join(t.TempDir(), "java8.tar.gz")
	if err := os.WriteFile(archive, data, 0600); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := extractArchive(context.Background(), archive, "tar.gz", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "jdk8u504-b01/man/ja/man1/java.1"))
	if err != nil || string(got) != "Japanese Java manual" {
		t.Fatalf("alias content: %q %v", got, err)
	}
	if err := filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			t.Errorf("materialized a link or special file: %s", p)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestJava8ManAliasRejectsUnsafeLayouts(t *testing.T) {
	base := []fixtureEntry{
		{name: "jdk8u504-b01/", kind: tar.TypeDir},
		{name: "jdk8u504-b01/man/", kind: tar.TypeDir},
		{name: "jdk8u504-b01/man/ja_JP.UTF-8/", kind: tar.TypeDir},
		{name: "jdk8u504-b01/man/ja_JP.UTF-8/java.1", data: "manual"},
	}
	for _, tc := range []struct {
		name           string
		extra          []fixtureEntry
		implicitSource bool
	}{
		{name: "escape", extra: []fixtureEntry{{name: "jdk8u504-b01/man/ja", kind: tar.TypeSymlink, link: "../../outside"}}},
		{name: "unrecognizedAlias", extra: []fixtureEntry{{name: "jdk8u504-b01/man/fr", kind: tar.TypeSymlink, link: "ja_JP.UTF-8"}}},
		{name: "hardDirectoryLink", extra: []fixtureEntry{{name: "jdk8u504-b01/man/ja", kind: tar.TypeLink, link: "jdk8u504-b01/man/ja_JP.UTF-8"}}},
		{name: "sourceLink", extra: []fixtureEntry{
			{name: "jdk8u504-b01/man/ja_JP.UTF-8/linked.1", kind: tar.TypeSymlink, link: "java.1"},
			{name: "jdk8u504-b01/man/ja", kind: tar.TypeSymlink, link: "ja_JP.UTF-8"},
		}},
		{name: "descendantCollision", extra: []fixtureEntry{
			{name: "jdk8u504-b01/man/ja/extra.1", data: "unrelated"},
			{name: "jdk8u504-b01/man/ja", kind: tar.TypeSymlink, link: "ja_JP.UTF-8"},
		}},
		{name: "caseCollision", extra: []fixtureEntry{
			{name: "jdk8u504-b01/man/JA/", kind: tar.TypeDir},
			{name: "jdk8u504-b01/man/ja", kind: tar.TypeSymlink, link: "ja_JP.UTF-8"},
		}},
		{name: "cycle", extra: []fixtureEntry{
			{name: "jdk8u504-b01/man/ja_JP.UTF-8/loop", kind: tar.TypeSymlink, link: "../ja"},
			{name: "jdk8u504-b01/man/ja", kind: tar.TypeSymlink, link: "ja_JP.UTF-8"},
		}},
		{name: "implicitSource", implicitSource: true, extra: []fixtureEntry{{name: "jdk8u504-b01/man/ja", kind: tar.TypeSymlink, link: "ja_JP.UTF-8"}}},
		{name: "multipleRoots", extra: []fixtureEntry{
			{name: "other/", kind: tar.TypeDir},
			{name: "jdk8u504-b01/man/ja", kind: tar.TypeSymlink, link: "ja_JP.UTF-8"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := append([]fixtureEntry{}, base...)
			if tc.implicitSource {
				entries = append(entries[:2], entries[3:]...)
			}
			entries = append(entries, tc.extra...)
			archive := filepath.Join(t.TempDir(), "java8.tar.gz")
			if err := os.WriteFile(archive, archiveFixture(t, "tar.gz", entries), 0600); err != nil {
				t.Fatal(err)
			}
			if err := extractArchive(context.Background(), archive, "tar.gz", t.TempDir()); err == nil {
				t.Fatal("accepted unsafe directory alias layout")
			}
		})
	}
}

// Losing either e.entry or e.file in recursive alias copying must not let the
// copied descendants bypass the same budgets as original archive entries.
func TestJava8ManAliasCopyBudgets(t *testing.T) {
	for _, limit := range []string{"count", "bytes", "cancel"} {
		t.Run(limit, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e := &extractor{ctx: ctx, root: t.TempDir(), names: map[string]string{}, entries: map[string]bool{}}
			for _, name := range []string{"jdk8u504-b01", "jdk8u504-b01/man", "jdk8u504-b01/man/ja_JP.UTF-8"} {
				if _, err := e.entry(name, true); err != nil {
					t.Fatal(err)
				}
			}
			source := filepath.Join(e.root, "jdk8u504-b01/man/ja_JP.UTF-8")
			if err := os.MkdirAll(source, 0700); err != nil {
				t.Fatal(err)
			}
			file, err := e.entry("jdk8u504-b01/man/ja_JP.UTF-8/java.1", false)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("manual"), 0600); err != nil {
				t.Fatal(err)
			}
			link := archiveLink{name: "jdk8u504-b01/man/ja", target: "ja_JP.UTF-8"}
			if _, err := e.entry(link.name, false); err != nil {
				t.Fatal(err)
			}
			switch limit {
			case "count":
				e.count = maxEntries
			case "bytes":
				e.total = maxExtractedBytes
			case "cancel":
				cancel()
			}
			err = e.copyManAlias(link, "jdk8u504-b01/man/ja_JP.UTF-8", source, []archiveLink{link})
			if err == nil {
				t.Fatal("alias copying bypassed limit")
			}
			if limit == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}
