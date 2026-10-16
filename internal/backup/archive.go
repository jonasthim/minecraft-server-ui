package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	CaptureMode  string            `json:"capture_mode"`
	WorldPresent bool              `json:"world_present"`
	LevelName    string            `json:"level_name"`
	Format       int               `json:"format"`
	InstanceID   string            `json:"instance_id"`
	Kind         domain.BackupKind `json:"kind"`
	Version      string            `json:"minecraft_version"`
	Flavor       domain.Flavor     `json:"flavor"`
	Loader       string            `json:"loader_version,omitempty"`
	Runtime      string            `json:"java_runtime_id,omitempty"`
	Layout       string            `json:"layout"`
	CreatedAt    time.Time         `json:"created_at"`
	Roots        []string          `json:"roots"`
	Files        []File            `json:"files"`
	Warnings     []string          `json:"warnings"`
}

const manifestName = "backup-manifest.json"

func writeArchive(ctx context.Context, stage *source, out io.Writer, m Manifest, lim limits) (err error) {
	w := zip.NewWriter(out)
	defer func() { err = errors.Join(err, w.Close()) }()
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if int64(len(raw)) > lim.pathBytes+int64(lim.entries)*128 {
		return errCorrupt
	}
	h := &zip.FileHeader{Name: manifestName, Method: zip.Deflate}
	h.SetMode(0600)
	f, err := w.CreateHeader(h)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	for _, entry := range m.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := safePath(entry.Path); err != nil {
			return err
		}
		f, err := stage.root.Open(entry.Path)
		if err != nil {
			return err
		}
		h := &zip.FileHeader{Name: "server/" + entry.Path, Method: zip.Deflate}
		h.SetMode(0600)
		out, err := w.CreateHeader(h)
		if err == nil {
			_, err = io.Copy(out, contextReader{ctx, io.LimitReader(f, entry.Size+1)})
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
	}
	return stage.verify()
}
func validateArchive(ctx context.Context, r io.ReaderAt, size int64, lim limits) (Manifest, error) {
	var m Manifest
	if size <= 0 || size > lim.archiveBytes {
		return m, errCorrupt
	}
	z, err := zip.NewReader(r, size)
	if err != nil {
		return m, errCorrupt
	}
	if len(z.File) > lim.entries+1 {
		return m, errCorrupt
	}
	entries := map[string]*zip.File{}
	fold := map[string]bool{}
	var manifest *zip.File
	var total, pathBytes int64
	for _, f := range z.File {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		if err := safePath(f.Name); err != nil {
			return m, err
		}
		if !f.Mode().IsRegular() || f.Flags&1 != 0 || fold[strings.ToLower(f.Name)] {
			return m, errCorrupt
		}
		fold[strings.ToLower(f.Name)] = true
		pathBytes += int64(len(f.Name))
		if pathBytes > lim.pathBytes {
			return m, errCorrupt
		}
		if f.Name == manifestName {
			manifest = f
			if f.UncompressedSize64 > uint64(lim.pathBytes+int64(lim.entries)*128) {
				return m, errCorrupt
			}
			continue
		}
		if !strings.HasPrefix(f.Name, "server/") || excluded(strings.TrimPrefix(f.Name, "server/")) {
			return m, errCorrupt
		}
		if f.UncompressedSize64 > uint64(lim.fileBytes) || f.UncompressedSize64 > uint64(lim.bytes-total) {
			return m, errCorrupt
		}
		total += int64(f.UncompressedSize64)
		entries[strings.TrimPrefix(f.Name, "server/")] = f
	}
	if manifest == nil {
		return m, errCorrupt
	}
	mf, err := manifest.Open()
	if err != nil {
		return m, errCorrupt
	}
	decoder := json.NewDecoder(contextReader{ctx, io.LimitReader(mf, int64(manifest.UncompressedSize64)+1)})
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&m)
	var extra any
	if err == nil && !errors.Is(decoder.Decode(&extra), io.EOF) {
		err = errCorrupt
	}
	err = errors.Join(err, mf.Close())
	if err != nil || m.Format != 1 || len(m.Files) != len(entries) || len(m.Roots) == 0 {
		return m, errCorrupt
	}
	candidate := domain.Instance{ID: m.InstanceID, Name: "backup", Flavor: m.Flavor, MinecraftVersion: m.Version, JavaMajor: 8, MemoryMiB: 256, GamePort: 1}
	if candidate.Validate() != nil || (m.Layout != "legacy" && m.Layout != "paper_legacy" && m.Layout != "namespaced_26_1") || (m.Kind != domain.BackupWorld && m.Kind != domain.BackupFull) || safePath(m.LevelName) != nil || strings.Contains(m.LevelName, "/") {
		return m, errCorrupt
	}
	if m.WorldPresent {
		if entries[m.LevelName+"/level.dat"] == nil {
			return m, errCorrupt
		}
	} else if m.Kind == domain.BackupWorld {
		return m, errCorrupt
	}
	roots := map[string]bool{}
	exactRoots := map[string]bool{}
	for _, root := range m.Roots {
		if safePath(root) != nil || strings.Contains(root, "/") || excluded(root) || roots[strings.ToLower(root)] {
			return m, errCorrupt
		}
		roots[strings.ToLower(root)] = true
		exactRoots[root] = true
	}
	seen := map[string]bool{}
	for _, entry := range m.Files {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		if safePath(entry.Path) != nil || !exactRoots[strings.Split(entry.Path, "/")[0]] || seen[entry.Path] || entry.Size < 0 || len(entry.SHA256) != 64 {
			return m, errCorrupt
		}
		seen[entry.Path] = true
		f := entries[entry.Path]
		if f == nil || uint64(entry.Size) != f.UncompressedSize64 {
			return m, errCorrupt
		}
		in, err := f.Open()
		if err != nil {
			return m, errCorrupt
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, contextReader{ctx, io.LimitReader(in, entry.Size+1)})
		closeErr := in.Close()
		if readErr != nil || closeErr != nil || n != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return m, errCorrupt
		}
	}
	// A file cannot also be an ancestor of another file.
	for name := range entries {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if fold["server/"+strings.ToLower(parent)] {
				return m, errCorrupt
			}
		}
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return m, nil
}
func extractArchive(ctx context.Context, r io.ReaderAt, size int64, dst *source, lim limits) (Manifest, error) {
	m, err := validateArchive(ctx, r, size, lim)
	if err != nil {
		return m, err
	}
	z, err := zip.NewReader(r, size)
	if err != nil {
		return m, err
	}
	for _, f := range z.File {
		if f.Name == manifestName {
			continue
		}
		name := strings.TrimPrefix(f.Name, "server/")
		if err := dst.root.MkdirAll(path.Dir(name), 0700); err != nil {
			return m, err
		}
		out, err := dst.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return m, err
		}
		in, err := f.Open()
		if err != nil {
			out.Close()
			return m, err
		}
		_, copyErr := io.Copy(out, contextReader{ctx, io.LimitReader(in, int64(f.UncompressedSize64)+1)})
		err = errors.Join(copyErr, in.Close(), out.Sync(), out.Close())
		if err != nil {
			return m, err
		}
	}
	return m, dst.verify()
}
