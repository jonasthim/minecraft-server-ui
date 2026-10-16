package world

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

// Ignoring directory entries allowed an added traversal, duplicate, or missing
// directory to pass validation despite the reviewed source inventory.
func TestValidateZIPRejectsDirectoryAndFileTampering(t *testing.T) {
	for _, tc := range []string{"valid", "unexpected-directory", "traversal-directory", "duplicate-directory", "missing-directory", "symlink-file", "changed-file", "duplicate-file"} {
		t.Run(tc, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "archive-*.zip")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			zw := zip.NewWriter(f)
			write := func(name string, mode os.FileMode, body string) {
				t.Helper()
				h := &zip.FileHeader{Name: name, Method: zip.Store}
				h.SetMode(mode)
				w, err := zw.CreateHeader(h)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = w.Write([]byte(body)); err != nil {
					t.Fatal(err)
				}
			}
			if tc != "missing-directory" {
				write("world/region/", os.ModeDir|0700, "")
			}
			mode, body := os.FileMode(0600), "saved"
			if tc == "symlink-file" {
				mode = os.ModeSymlink | 0600
			}
			if tc == "changed-file" {
				body = "other"
			}
			write("world/region/r.0.0.mca", mode, body)
			switch tc {
			case "unexpected-directory":
				write("world/extra/", os.ModeDir|0700, "")
			case "traversal-directory":
				write("../outside/", os.ModeDir|0700, "")
			case "duplicate-directory":
				write("world/region/", os.ModeDir|0700, "")
			case "duplicate-file":
				write("world/region/r.0.0.mca", 0600, "saved")
			}
			if err = zw.Close(); err != nil {
				t.Fatal(err)
			}
			info, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256([]byte("saved"))
			expected := map[string]fileDigest{
				"world/region/":          {Path: "world/region/", directory: true},
				"world/region/r.0.0.mca": {Path: "world/region/r.0.0.mca", Size: 5, SHA256: hex.EncodeToString(hash[:])},
			}
			err = validateZIP(context.Background(), &Archive{file: f, Size: info.Size()}, expected)
			if tc == "valid" && err != nil {
				t.Fatal(err)
			}
			if tc != "valid" && err == nil {
				t.Fatal("tampered archive accepted")
			}
		})
	}
}
