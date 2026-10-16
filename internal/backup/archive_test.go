package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveTraversalLinksAndCorruptionRejected(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "world/../escape", "world\\escape", "world/CON", "world/a:stream", "world/a.", "world//x"} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			w := zip.NewWriter(&b)
			h := &zip.FileHeader{Name: name, Method: zip.Store}
			f, _ := w.CreateHeader(h)
			f.Write([]byte("data"))
			w.Close()
			if _, err := validateArchive(context.Background(), bytes.NewReader(b.Bytes()), int64(b.Len()), defaultLimits); err == nil {
				t.Fatal("accepted malicious archive")
			}
		})
	}
}
func TestCaptureAndArchiveRoundTrip(t *testing.T) {
	source := t.TempDir()
	stage := t.TempDir()
	os.MkdirAll(filepath.Join(source, "world/region"), 0700)
	os.WriteFile(filepath.Join(source, "world/level.dat"), []byte("level"), 0600)
	os.WriteFile(filepath.Join(source, "world/region/r.0.0.mca"), []byte("region"), 0600)
	os.WriteFile(filepath.Join(source, "world/.hidden"), []byte("hidden"), 0600)
	src, err := openSource(source)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := openSource(stage)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	files, err := captureTree(context.Background(), src, dst, []string{"world"}, false, defaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files=%v", files)
	}
	m := Manifest{WorldPresent: true, LevelName: "world", InstanceID: "alpha", Kind: domain.BackupWorld, Version: "1.16.5", Flavor: domain.FlavorVanilla, Layout: "legacy", Format: 1, Roots: []string{"world"}, Files: files}
	var b bytes.Buffer
	if err := writeArchive(context.Background(), dst, &b, m, defaultLimits); err != nil {
		t.Fatal(err)
	}
	result, err := validateArchive(context.Background(), bytes.NewReader(b.Bytes()), int64(b.Len()), defaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 3 {
		t.Fatal("files lost")
	}
	raw := b.Bytes()
	z, _ := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	offset, _ := z.File[len(z.File)-1].DataOffset()
	raw[offset] ^= 0xff
	if _, err := validateArchive(context.Background(), bytes.NewReader(raw), int64(len(raw)), defaultLimits); err == nil {
		t.Fatal("accepted corruption")
	}
}
func TestCaptureRejectsSourceSymlinkAndManagerDB(t *testing.T) {
	source := t.TempDir()
	stage := t.TempDir()
	os.WriteFile(filepath.Join(source, "manager.db"), []byte("private"), 0600)
	os.WriteFile(filepath.Join(source, ".save"), []byte("save"), 0600)
	os.Symlink(t.TempDir(), filepath.Join(source, "escape"))
	src, err := openSource(source)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := openSource(stage)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if _, err := captureTree(context.Background(), src, dst, nil, true, defaultLimits); err == nil {
		t.Fatal("accepted symlink")
	}
	os.Remove(filepath.Join(source, "escape"))
	dst.Close()
	dst, err = openSource(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	files, err := captureTree(context.Background(), src, dst, nil, true, defaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != ".save" {
		t.Fatalf("manager data included or hidden saves lost: %v", files)
	}
}

func TestWorldArchiveRequiresConfiguredSaveMarker(t *testing.T) {
	stage, err := openSource(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	m := Manifest{Format: 1, InstanceID: "alpha", Kind: domain.BackupWorld, Version: "1.16.5", Flavor: domain.FlavorVanilla, Layout: "legacy", LevelName: "world", WorldPresent: true, Roots: []string{"world"}, Files: []File{}}
	var b bytes.Buffer
	if err = writeArchive(t.Context(), stage, &b, m, defaultLimits); err != nil {
		t.Fatal(err)
	}
	if _, err = validateArchive(t.Context(), bytes.NewReader(b.Bytes()), int64(b.Len()), defaultLimits); err == nil {
		t.Fatal("archive with vanished level.dat accepted")
	}
}
