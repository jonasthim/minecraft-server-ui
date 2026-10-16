package world

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// Two active captures/transfers cap temporary export disk usage across services.
var exportSlots = make(chan struct{}, 2)

// Admission is excluded during startup orphan reconciliation. Transfers retain
// their slot until Close, so reconciliation cannot mistake a live file for an orphan.
var exportAdmission sync.Mutex

// Archive owns its staged file and export admission until Close. Callers must
// close on success, cancellation, and disconnected/failed HTTP transfers.
type Archive struct {
	file             *os.File
	staging          *os.Root
	name             string
	once             sync.Once
	closeErr         error
	Size             int64
	SHA256, Filename string
}

func (a *Archive) Read(p []byte) (int, error)                { return a.file.Read(p) }
func (a *Archive) ReadAt(p []byte, off int64) (int, error)   { return a.file.ReadAt(p, off) }
func (a *Archive) Seek(off int64, whence int) (int64, error) { return a.file.Seek(off, whence) }
func (a *Archive) Close() error {
	a.once.Do(func() {
		fileErr := a.file.Close()
		removeErr := a.staging.Remove(a.name)
		rootErr := a.staging.Close()
		a.closeErr = errors.Join(fileErr, removeErr, rootErr)
		<-exportSlots
	})
	return a.closeErr
}

type fileDigest struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	directory bool
}
type exportManifest struct {
	Format    int          `json:"format"`
	World     Discovery    `json:"world"`
	Files     []fileDigest `json:"files"`
	CreatedAt time.Time    `json:"created_at"`
}
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}

type boundedWriter struct {
	w         io.Writer
	remaining int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, bound()
	}
	n, e := w.w.Write(p)
	w.remaining -= int64(n)
	return n, e
}
func sameEntry(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// readEntry binds the opened file to the inventory identity before reading and
// checks both the handle and current path after reading. A second digest pass
// catches writes that preserve size/mtime; cross-file atomicity is not claimed.
func readEntry(ctx context.Context, r *os.Root, entry snapshotEntry, w io.Writer) (string, error) {
	before, e := r.Lstat(entry.name)
	if e != nil || !sameEntry(entry.info, before) || !before.Mode().IsRegular() {
		return "", changed()
	}
	f, e := openRead(r, entry.name, false)
	if e != nil {
		return "", e
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !sameEntry(entry.info, opened) {
		return "", changed()
	}
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(w, h), contextReader{ctx, io.LimitReader(f, entry.info.Size()+1)})
	if e != nil {
		return "", e
	}
	if n != entry.info.Size() {
		return "", changed()
	}
	after, e := f.Stat()
	current, ce := r.Lstat(entry.name)
	if e != nil || ce != nil || !sameEntry(entry.info, after) || !sameEntry(entry.info, current) || !current.Mode().IsRegular() {
		return "", changed()
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (s *Service) stage(id string) (*Archive, error) {
	inst, e := s.openInstance(id)
	if e != nil {
		return nil, e
	}
	defer inst.Close()
	if e = inst.Mkdir("backups", 0700); e != nil && !os.IsExist(e) {
		return nil, e
	}
	backups, e := openChild(inst, "backups")
	if e != nil {
		return nil, e
	}
	defer backups.Close()
	if e = backups.Mkdir(".exports", 0700); e != nil && !os.IsExist(e) {
		return nil, e
	}
	staging, e := openChild(backups, ".exports")
	if e != nil {
		return nil, e
	}
	var token [16]byte
	if _, e = rand.Read(token[:]); e != nil {
		staging.Close()
		return nil, e
	}
	name := hex.EncodeToString(token[:]) + ".zip"
	f, e := staging.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		staging.Close()
		return nil, e
	}
	return &Archive{file: f, staging: staging, name: name, Filename: id + "-world.zip"}, nil
}
func (s *Service) validateRoots(ctx context.Context, id string, roots []worldRoot, digests map[string]string) error {
	inst, e := s.openInstance(id)
	if e != nil {
		return e
	}
	defer inst.Close()
	server, e := openChild(inst, "server")
	if e != nil {
		return e
	}
	defer server.Close()
	detail, e := s.instances.Get(ctx, id)
	if e != nil {
		return e
	}
	expectedRoots := map[string]bool{}
	for _, root := range roots {
		expectedRoots[root.name] = true
	}
	names := []string{detail.Config.LevelName}
	if detail.Instance.Flavor == domain.FlavorPaper {
		names = append(names, detail.Config.LevelName+"_nether", detail.Config.LevelName+"_the_end")
	}
	for _, name := range names {
		info, se := server.Lstat(name)
		if os.IsNotExist(se) {
			if expectedRoots[name] {
				return changed()
			}
			continue
		}
		if se != nil {
			return se
		}
		if !expectedRoots[name] || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return changed()
		}
	}
	for _, root := range roots {
		pinned, e := root.root.Stat(".")
		current, ce := server.Lstat(root.name)
		if e != nil || ce != nil || !os.SameFile(pinned, current) || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
			return changed()
		}
		now, e := s.inventory(ctx, root.root)
		if e != nil {
			return e
		}
		if len(now) != len(root.entries) {
			return changed()
		}
		for i, entry := range now {
			old := root.entries[i]
			if old.name != entry.name || old.omitted != entry.omitted || !sameEntry(old.info, entry.info) {
				return changed()
			}
			if entry.omitted || entry.info.IsDir() {
				continue
			}
			digest, e := readEntry(ctx, root.root, entry, io.Discard)
			if e != nil {
				return e
			}
			if digest != digests[path.Join(root.name, entry.name)] {
				return changed()
			}
		}
	}
	return nil
}
func validateZIP(ctx context.Context, a *Archive, expected map[string]fileDigest) error {
	zr, e := zip.NewReader(a, a.Size)
	if e != nil {
		return fmt.Errorf("open staged world archive: %w", e)
	}
	seen := map[string]bool{}
	for _, f := range zr.File {
		if e = ctx.Err(); e != nil {
			return e
		}
		want, ok := expected[f.Name]
		if !ok || seen[f.Name] || f.UncompressedSize64 > uint64(defaultLimits.fileBytes) || int64(f.UncompressedSize64) != want.Size {
			return domain.E(domain.CodeValidationFailed, "staged archive structure mismatch")
		}
		name := f.Name
		if want.directory {
			if !strings.HasSuffix(name, "/") || !f.Mode().IsDir() || f.Mode()&os.ModeType != os.ModeDir || f.UncompressedSize64 != 0 || f.CompressedSize64 != 0 {
				return unsafe()
			}
			name = strings.TrimSuffix(name, "/")
		} else if !f.Mode().IsRegular() || strings.HasSuffix(name, "/") {
			return unsafe()
		}
		if len(name) > 4096 {
			return unsafe()
		}
		for _, part := range strings.Split(name, "/") {
			if e = safePart(part); e != nil {
				return e
			}
		}
		seen[f.Name] = true
		if want.directory {
			continue
		}
		r, e := f.Open()
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(h, contextReader{ctx, io.LimitReader(r, want.Size+1)})
		closeErr := r.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
			return domain.E(domain.CodeValidationFailed, "staged archive digest mismatch")
		}
	}
	if len(seen) != len(expected) {
		return domain.E(domain.CodeValidationFailed, "staged archive has missing files")
	}
	return nil
}
func (s *Service) Capture(ctx context.Context, actor domain.User, id string) (*Archive, error) {
	if actor.Disabled || actor.ID <= 0 || !actor.Role.AtLeast(domain.RoleOperator) {
		return nil, domain.E(domain.CodeForbidden, "operator access required to download worlds")
	}
	if s.db == nil || s.instances == nil || s.reservation == nil {
		return nil, domain.E(domain.CodeUnavailable, "world export is not configured")
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if !exportAdmission.TryLock() {
		return nil, domain.E(domain.CodeInstanceBusy, "world export reconciliation is active; retry when it finishes")
	}
	select {
	case exportSlots <- struct{}{}:
		exportAdmission.Unlock()
	default:
		exportAdmission.Unlock()
		return nil, domain.E(domain.CodeInstanceBusy, "two world exports are already active; retry after a transfer finishes")
	}
	owned := true
	defer func() {
		if owned {
			<-exportSlots
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, s.limits.capture)
	defer cancel()
	var a *Archive
	e := s.reservation.WithIdle(ctx, id, func() (captureErr error) {
		d, roots, e := s.discover(ctx, id, true)
		if e != nil {
			return e
		}
		defer closeRoots(roots)
		if d.State == "absent" {
			return domain.NotFound("saved world")
		}
		if strings.EqualFold(d.LevelName, "world-export.json") {
			return domain.E(domain.CodeConflict, "world root collides with export metadata name")
		}
		if d.State != "present" {
			return domain.E(domain.CodeConflict, "world layout is ambiguous or unsupported; export refused")
		}
		a, e = s.stage(id)
		if e != nil {
			return e
		}
		owned = false
		success := false
		defer func() {
			if !success {
				if cleanupErr := a.Close(); cleanupErr != nil {
					captureErr = errors.Join(captureErr, fmt.Errorf("cleanup failed world export: %w", cleanupErr))
				}
				a = nil
			}
		}()
		zw := zip.NewWriter(&boundedWriter{a.file, s.limits.archiveBytes})
		closed := false
		defer func() {
			if !closed {
				_ = zw.Close()
			}
		}()
		manifest := exportManifest{1, d, []fileDigest{}, time.Now().UTC()}
		expected := map[string]fileDigest{}
		sourceDigests := map[string]string{}
		for _, root := range roots {
			for _, entry := range root.entries {
				if e = ctx.Err(); e != nil {
					return e
				}
				if entry.omitted {
					continue
				}
				name := path.Join(root.name, entry.name)
				header := &zip.FileHeader{Name: name, Method: zip.Store}
				if entry.info.IsDir() {
					header.Name += "/"
					header.SetMode(os.ModeDir | 0700)
				} else {
					header.SetMode(0600)
				}
				w, e := zw.CreateHeader(header)
				if e != nil {
					return e
				}
				if entry.info.IsDir() {
					expected[header.Name] = fileDigest{Path: header.Name, directory: true}
					continue
				}
				digest, e := readEntry(ctx, root.root, entry, w)
				if e != nil {
					return e
				}
				item := fileDigest{Path: name, Size: entry.info.Size(), SHA256: digest}
				expected[name] = item
				sourceDigests[name] = digest
				manifest.Files = append(manifest.Files, item)
			}
		}
		if e = s.validateRoots(ctx, id, roots, sourceDigests); e != nil {
			return e
		}
		body, e := json.Marshal(manifest)
		if e != nil {
			return e
		}
		w, e := zw.Create("world-export.json")
		if e != nil {
			return e
		}
		if _, e = w.Write(body); e != nil {
			return e
		}
		h := sha256.Sum256(body)
		expected["world-export.json"] = fileDigest{Path: "world-export.json", Size: int64(len(body)), SHA256: hex.EncodeToString(h[:])}
		e = zw.Close()
		closed = true
		if e != nil {
			return e
		}
		info, e := a.file.Stat()
		if e != nil {
			return e
		}
		a.Size = info.Size()
		if e = validateZIP(ctx, a, expected); e != nil {
			return e
		}
		if _, e = a.Seek(0, io.SeekStart); e != nil {
			return e
		}
		archiveHash := sha256.New()
		if _, e = io.Copy(archiveHash, contextReader{ctx, a}); e != nil {
			return e
		}
		a.SHA256 = hex.EncodeToString(archiveHash.Sum(nil))
		if _, e = a.Seek(0, io.SeekStart); e != nil {
			return e
		}
		details, e := json.Marshal(struct {
			Bytes  int64  `json:"bytes"`
			SHA256 string `json:"sha256"`
			Phase  string `json:"phase"`
		}{a.Size, a.SHA256, "validated_archive_prepared"})
		if e != nil {
			return e
		}
		if _, e = s.db.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,instance_id,target,details_json) VALUES(?,?,?,'world.export_prepared',?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor.ID, actor.Username, id, id, string(details)); e != nil {
			return fmt.Errorf("audit prepared world export: %w", e)
		}
		success = true
		return nil
	})
	if e != nil {
		return nil, e
	}
	return a, nil
}
